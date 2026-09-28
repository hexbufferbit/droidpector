// Package ipc exposes the application core to the UI over an authenticated
// HTTP + WebSocket API bound to 127.0.0.1 only (see SECURITY.md).
package ipc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/droidpector/apkinspector/src/content"
	"github.com/droidpector/apkinspector/src/core"
	"github.com/droidpector/apkinspector/src/display"
	"github.com/droidpector/apkinspector/src/export"
	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/platform"
	"github.com/droidpector/apkinspector/src/query"
	"github.com/droidpector/apkinspector/src/storage"
)

const cookieName = "droidpector_session"

// Server is the local API server.
type Server struct {
	app    *core.App
	ui     fs.FS // built UI assets (may be nil in API-only tests)
	token  string
	log    *slog.Logger
	srv    *http.Server
	ln     net.Listener
	origin string
}

// NewServer listens on 127.0.0.1:port (0 = random) with a fresh 256-bit token.
func NewServer(app *core.App, ui fs.FS, port int, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("starting the local API: %w", err)
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{app: app, ui: ui, token: hex.EncodeToString(tok), log: log, ln: ln, origin: "http://" + ln.Addr().String()}
	s.srv = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 30 * time.Second}
	return s, nil
}

// URL returns the one-time bootstrap URL the host window opens (it sets the
// session cookie and redirects to the UI).
func (s *Server) URL() string { return s.origin + "/auth?token=" + s.token }

// BaseURL returns the server origin.
func (s *Server) BaseURL() string { return s.origin }

// Token returns the API token (for bearer auth in tests and tools).
func (s *Server) Token() string { return s.token }

// Serve blocks serving requests.
func (s *Server) Serve() error {
	err := s.srv.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	api := func(pattern string, h func(http.ResponseWriter, *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := h(w, r); err != nil {
				s.writeError(w, err)
			}
		})
	}
	mux.HandleFunc("GET /auth", s.handleAuth)

	api("GET /api/info", s.info)
	api("GET /api/status", func(w http.ResponseWriter, r *http.Request) error { return writeJSON(w, s.app.Sandbox.Status()) })
	api("POST /api/sandbox/start", s.sandboxStart)
	api("POST /api/sandbox/stop", s.async(func(ctx context.Context, _ *http.Request) error { return s.app.Sandbox.Stop(ctx) }))
	api("POST /api/sandbox/restart", s.async(func(ctx context.Context, _ *http.Request) error { return s.app.Sandbox.Restart(ctx) }))
	api("POST /api/sandbox/app-only", func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		s.app.Sandbox.SetAppOnly(ctx, req.Enabled)
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	api("POST /api/sandbox/reset", s.async(func(ctx context.Context, _ *http.Request) error { return s.app.Sandbox.Reset(ctx) }))

	api("GET /api/snapshots", func(w http.ResponseWriter, r *http.Request) error {
		snaps, err := s.app.Sandbox.Snapshots(r.Context())
		if err != nil {
			return err
		}
		return writeJSON(w, snaps)
	})
	api("POST /api/snapshots", func(w http.ResponseWriter, r *http.Request) error {
		var req struct{ Name string }
		if err := readJSON(r, &req); err != nil {
			return err
		}
		if err := s.app.Sandbox.SaveSnapshot(r.Context(), req.Name); err != nil {
			return err
		}
		return writeJSON(w, map[string]string{"name": req.Name})
	})
	api("POST /api/snapshots/{name}/restore", s.async(func(ctx context.Context, r *http.Request) error {
		return s.app.Sandbox.RestoreSnapshot(ctx, r.PathValue("name"))
	}))
	api("DELETE /api/snapshots/{name}", func(w http.ResponseWriter, r *http.Request) error {
		if err := s.app.Sandbox.DeleteSnapshot(r.Context(), r.PathValue("name")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})

	api("POST /api/apks", s.uploadAPK)
	api("GET /api/apks", func(w http.ResponseWriter, r *http.Request) error { return writeJSON(w, s.app.APKs.List()) })
	api("POST /api/apks/{id}/run", s.async(func(ctx context.Context, r *http.Request) error { return s.app.APKs.Run(ctx, r.PathValue("id")) }))
	api("POST /api/apks/{id}/install", s.async(func(ctx context.Context, r *http.Request) error { return s.app.APKs.Install(ctx, r.PathValue("id")) }))
	api("POST /api/apks/{id}/reinstall", s.async(func(ctx context.Context, r *http.Request) error { return s.app.APKs.Reinstall(ctx, r.PathValue("id")) }))
	api("POST /api/apps/{pkg}/launch", s.sync(func(ctx context.Context, r *http.Request) error { return s.app.APKs.Launch(ctx, r.PathValue("pkg")) }))
	api("POST /api/apps/{pkg}/stop", s.sync(func(ctx context.Context, r *http.Request) error { return s.app.APKs.Stop(ctx, r.PathValue("pkg")) }))
	api("POST /api/apps/{pkg}/clear", s.sync(func(ctx context.Context, r *http.Request) error { return s.app.APKs.ClearData(ctx, r.PathValue("pkg")) }))
	api("POST /api/apps/{pkg}/uninstall", s.sync(func(ctx context.Context, r *http.Request) error { return s.app.APKs.Uninstall(ctx, r.PathValue("pkg")) }))
	api("POST /api/display/paste", s.paste)
	api("POST /api/display/rotate", func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Orientation int `json:"orientation"`
		}
		if err := readJSON(r, &req); err != nil {
			return err
		}
		if req.Orientation < 0 || req.Orientation > 3 {
			return &core.UserError{Code: "bad_request", Title: "Orientation must be 0, 1, 2 or 3 (quarter turns)."}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.app.Sandbox.Rotate(ctx, req.Orientation); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})

	api("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) error {
		list, err := s.app.Sessions.List(r.Context())
		if err != nil {
			return err
		}
		if list == nil {
			list = []*model.Session{}
		}
		return writeJSON(w, list)
	})
	api("POST /api/capture/start", func(w http.ResponseWriter, r *http.Request) error {
		if !s.app.Sandbox.Running() {
			return &core.UserError{Code: "not_running", Title: "Start the sandbox to capture traffic."}
		}
		sess, err := s.app.Sessions.Start(r.Context(), s.app.Sandbox.Profile().Name)
		if err != nil {
			return err
		}
		s.app.Hub.StatusChanged(s.statusWithSession(sess.ID))
		return writeJSON(w, sess)
	})
	api("POST /api/capture/stop", func(w http.ResponseWriter, r *http.Request) error {
		s.app.Sessions.End(r.Context())
		s.app.Hub.StatusChanged(s.statusWithSession(""))
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	api("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) error {
		sess, err := s.app.Sessions.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		return writeJSON(w, sess)
	})
	api("POST /api/sessions/{id}/save", func(w http.ResponseWriter, r *http.Request) error {
		var req struct{ Name string }
		readJSON(r, &req)
		sess, err := s.app.Sessions.Save(r.Context(), r.PathValue("id"), req.Name)
		if err != nil {
			return err
		}
		return writeJSON(w, sess)
	})
	api("POST /api/sessions/{id}/clear", func(w http.ResponseWriter, r *http.Request) error {
		if err := s.app.Sessions.Clear(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		s.app.Hub.EventsChanged(r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	api("DELETE /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) error {
		if err := s.app.Sessions.Delete(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	api("GET /api/sessions/{id}/har", s.exportHAR)
	api("GET /api/sessions/{id}/events", s.listEvents)

	api("GET /api/events/{id}", s.eventDetail)
	api("GET /api/events/{id}/body/{part}", s.eventBody)
	api("GET /api/events/{id}/frames", func(w http.ResponseWriter, r *http.Request) error {
		from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		frames, err := s.app.Store.Frames(r.Context(), r.PathValue("id"), from, 1000)
		if err != nil {
			return err
		}
		if frames == nil {
			frames = []model.WSFrame{}
		}
		return writeJSON(w, frames)
	})
	api("GET /api/events/{id}/code/{gen}", s.eventCode)
	api("GET /api/events/{id}/har", s.eventHAR)
	api("POST /api/events/{id}/replay", func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		e, err := s.app.Replay(ctx, r.PathValue("id"))
		if err != nil {
			return err
		}
		return writeJSON(w, e.Summary())
	})

	api("GET /api/diagnostics", func(w http.ResponseWriter, r *http.Request) error {
		return writeJSON(w, map[string]any{"gateway": s.app.Gateway.Stats(), "storage": s.app.Writer.Stats(), "status": s.app.Sandbox.Status()})
	})
	api("GET /api/diagnostics/bundle", s.diagnosticBundle)

	mux.HandleFunc("GET /api/ws", s.wsEvents)
	mux.HandleFunc("GET /api/display", s.wsDisplay)
	if s.ui != nil {
		mux.Handle("GET /", http.FileServerFS(s.ui))
	}
	return s.secure(mux)
}

// ---- security ----------------------------------------------------------------

// secure enforces loopback Host/Origin (DNS-rebinding defence) and the token.
func (s *Server) secure(next http.Handler) http.Handler {
	host := strings.TrimPrefix(s.origin, "http://")
	_, port, _ := net.SplitHostPort(host)
	allowedHosts := map[string]bool{host: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != s.origin && o != "http://localhost:"+port {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; connect-src 'self' ws://"+host+" ws://localhost:"+port+"; frame-ancestors 'none'")
		if r.URL.Path == "/auth" || !strings.HasPrefix(r.URL.Path, "/api/") && s.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.authorized(r) {
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "droidpector: open the application window to use the inspector.", http.StatusUnauthorized)
				return
			}
			writeErr(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid API token.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	tok := ""
	if c, err := r.Cookie(cookieName); err == nil {
		tok = c.Value
	} else if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok = strings.TrimPrefix(h, "Bearer ")
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.token)) != 1 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

// ---- helpers -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	return json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return &core.UserError{Code: "bad_request", Title: "Invalid request body.", Details: err.Error()}
	}
	return nil
}

type apiError struct {
	Error core.ErrorInfo `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, title string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(apiError{Error: core.ErrorInfo{Code: code, Title: title}})
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	var ue *core.UserError
	var pe *query.ParseError
	status := http.StatusInternalServerError
	info := core.ErrorInfo{Title: err.Error(), Code: "internal"}
	switch {
	case errors.As(err, &ue):
		status = http.StatusConflict
		if ue.Code == "bad_request" || ue.Code == "bad_tag" || ue.Code == "not_apk" {
			status = http.StatusBadRequest
		}
		info = core.ErrorInfo{Title: ue.Title, Causes: ue.Causes, Details: ue.Details, Code: ue.Code}
	case errors.As(err, &pe):
		status = http.StatusBadRequest
		info = core.ErrorInfo{Title: pe.Error(), Code: "bad_filter"}
	case errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		info = core.ErrorInfo{Title: "Not found.", Code: "not_found"}
	case errors.Is(err, storage.ErrBlobNotFound):
		status = http.StatusNotFound
		info = core.ErrorInfo{Title: "The body is no longer stored (removed by the retention policy).", Code: "body_missing"}
	default:
		s.log.Error("API request failed", "err", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(apiError{Error: info})
}

// async runs long operations (boot, install) in the background: the response
// is 202 and progress/errors are published through the status stream.
func (s *Server) async(op func(context.Context, *http.Request) error) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		go func() {
			if err := op(context.Background(), r); err != nil {
				s.log.Warn("background operation failed", "path", r.URL.Path, "err", err)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
		return writeJSON(w, map[string]string{"status": "accepted"})
	}
}

func (s *Server) sync(op func(context.Context, *http.Request) error) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if err := op(ctx, r); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

func (s *Server) statusWithSession(id string) core.Status {
	st := s.app.Sandbox.Status()
	st.SessionID = id
	st.CaptureActive = id != "" && s.app.Sandbox.Running()
	return st
}

// ---- handlers --------------------------------------------------------------------

func (s *Server) info(w http.ResponseWriter, r *http.Request) error {
	var runtimes []map[string]any
	for _, p := range s.app.Profiles {
		runtimes = append(runtimes, map[string]any{"name": p.Name, "android": p.Android, "sdk": p.SDK, "abis": p.ABIs, "translation": p.Translation, "description": p.Description})
	}
	var gens []map[string]string
	for _, g := range export.Generators() {
		gens = append(gens, map[string]string{"id": g.ID(), "label": g.Label()})
	}
	return writeJSON(w, map[string]any{
		"version": s.app.Version, "runtimes": runtimes, "generators": gens,
		"config": map[string]any{"inspectHttps": s.app.Config.InspectHTTPS, "maxBodyMB": s.app.Config.MaxBodyMB, "memoryMB": s.app.Config.MemoryMB},
	})
}

func (s *Server) sandboxStart(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Runtime string `json:"runtime"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	go s.app.Sandbox.Start(context.Background(), req.Runtime)
	w.WriteHeader(http.StatusAccepted)
	return writeJSON(w, map[string]string{"status": "accepted"})
}

func (s *Server) uploadAPK(w http.ResponseWriter, r *http.Request) error {
	var body io.Reader = r.Body
	name := r.Header.Get("X-File-Name")
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct == "multipart/form-data" {
		mr, err := r.MultipartReader()
		if err != nil {
			return &core.UserError{Code: "bad_request", Title: "Invalid upload.", Details: err.Error()}
		}
		part, err := mr.NextPart()
		if err != nil {
			return &core.UserError{Code: "bad_request", Title: "The upload contained no file."}
		}
		defer part.Close()
		body, name = part, part.FileName()
	} else if n, err := url.QueryUnescape(name); err == nil {
		name = n
	}
	e, err := s.app.APKs.Add(body, name)
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	return writeJSON(w, e)
}

func (s *Server) paste(w http.ResponseWriter, r *http.Request) error {
	var req struct{ Text string }
	if err := readJSON(r, &req); err != nil {
		return err
	}
	dev, err := s.app.Sandbox.Device()
	if err != nil {
		return err
	}
	if err := dev.InputText(r.Context(), req.Text); err != nil {
		return &core.UserError{Code: "paste", Title: "The text could not be pasted into Android.", Details: err.Error()}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, err := s.app.Query.Query(r.Context(), query.Request{
		SessionID: r.PathValue("id"), Filter: q.Get("filter"), Quick: q.Get("quick"),
		Offset: offset, Limit: limit, Sort: q.Get("sort"), Desc: q.Get("desc") == "1" || q.Get("desc") == "true",
	})
	if err != nil {
		return err
	}
	return writeJSON(w, page)
}

// EventDetail is an event plus derived views for the detail tabs.
type EventDetail struct {
	*model.Event
	URL          string             `json:"url,omitempty"`
	QueryParams  []model.QueryParam `json:"queryParams,omitempty"`
	RequestKind  content.Kind       `json:"requestKind,omitempty"`
	ResponseKind content.Kind       `json:"responseKind,omitempty"`
	Replayable   bool               `json:"replayable"`
}

func (s *Server) eventDetail(w http.ResponseWriter, r *http.Request) error {
	e, err := s.app.Event(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	d := EventDetail{Event: e, URL: e.URL(), QueryParams: model.ParseQuery(e.Query)}
	d.Replayable = e.Kind == model.KindHTTP && !e.Encrypted && e.Method != "" && (e.RequestBody == nil || !e.RequestBody.Truncated)
	if b, err := s.decodedBody(e, "request", 256<<10); err == nil {
		d.RequestKind = content.Classify(e.RequestHeaders.Get("Content-Type"), b)
	}
	if b, err := s.decodedBody(e, "response", 256<<10); err == nil {
		d.ResponseKind = content.Classify(e.ResponseHeaders.Get("Content-Type"), b)
	}
	return writeJSON(w, d)
}

func (s *Server) decodedBody(e *model.Event, part string, limit int64) ([]byte, error) {
	ref := e.ResponseBody
	if part == "request" {
		ref = e.RequestBody
	}
	if ref == nil {
		return nil, nil
	}
	raw, err := s.app.Store.ReadBody(ref)
	if err != nil {
		return nil, err
	}
	if ref.Encoding == "" {
		return raw, nil
	}
	dec, err := content.Decode(ref.Encoding, raw, limit)
	if err != nil && dec == nil {
		return raw, nil
	}
	return dec, nil
}

// eventBody serves a body. ?decode=1 removes Content-Encoding; ?download=1
// forces a file download.
func (s *Server) eventBody(w http.ResponseWriter, r *http.Request) error {
	e, err := s.app.Event(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	part := r.PathValue("part")
	var ref *model.BodyRef
	var ct string
	switch part {
	case "request":
		ref, ct = e.RequestBody, e.RequestHeaders.Get("Content-Type")
	case "response":
		ref, ct = e.ResponseBody, e.ResponseHeaders.Get("Content-Type")
	default:
		return &core.UserError{Code: "bad_request", Title: "Body part must be request or response."}
	}
	if ref == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	raw, err := s.app.Store.ReadBody(ref)
	if err != nil {
		return err
	}
	body := raw
	if r.URL.Query().Get("decode") != "0" && ref.Encoding != "" {
		if dec, derr := content.Decode(ref.Encoding, raw, 512<<20); dec != nil {
			body = dec
			if derr != nil {
				w.Header().Set("X-Decode-Warning", derr.Error())
			}
		} else {
			w.Header().Set("X-Decode-Warning", "could not decode "+ref.Encoding)
		}
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	// Bodies are hostile content: never let the webview render them as a page.
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data:")
	w.Header().Set("X-Body-Kind", string(content.Classify(ct, body)))
	w.Header().Set("X-Body-Truncated", strconv.FormatBool(ref.Truncated))
	w.Header().Set("X-Body-Size", strconv.FormatInt(ref.Size, 10))
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": e.ID + "-" + part + ".bin"}))
	}
	_, err = w.Write(body)
	return err
}

func (s *Server) eventCode(w http.ResponseWriter, r *http.Request) error {
	gen, ok := export.GeneratorByID(r.PathValue("gen"))
	if !ok {
		return &core.UserError{Code: "bad_request", Title: "Unknown code generator."}
	}
	e, err := s.app.Event(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	var body []byte
	if e.RequestBody != nil {
		if body, err = s.app.Store.ReadBody(e.RequestBody); err != nil {
			return err
		}
	}
	req, err := export.RequestFromEvent(e, body)
	if err != nil {
		return &core.UserError{Code: "not_exportable", Title: err.Error()}
	}
	out, err := gen.Generate(req)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, err = io.WriteString(w, out)
	return err
}

func (s *Server) eventHAR(w http.ResponseWriter, r *http.Request) error {
	e, err := s.app.Event(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "request-" + e.ID + ".har"}))
	_, err = export.WriteHAR(w, export.HAROptions{Creator: "droidpector", Version: s.app.Version, IncludeBodies: true},
		func(yield func(*model.Event, error) bool) { yield(e, nil) }, s.app.Store)
	return err
}

func (s *Server) exportHAR(w http.ResponseWriter, r *http.Request) error {
	sess, err := s.app.Sessions.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": fmt.Sprintf("session-%d-%s.har", sess.Number, sess.StartedAt.Format("20060102-150405"))}))
	_, err = s.app.Sessions.ExportHAR(r.Context(), sess.ID, w)
	return err
}

func (s *Server) diagnosticBundle(w http.ResponseWriter, r *http.Request) error {
	diag, _ := json.MarshalIndent(map[string]any{"gateway": s.app.Gateway.Stats(), "storage": s.app.Writer.Stats(),
		"status": s.app.Sandbox.Status(), "version": s.app.Version}, "", "  ")
	cfg, _ := json.MarshalIndent(s.app.Config, "", "  ")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": "droidpector-diagnostics-" + time.Now().Format("20060102-150405") + ".zip"}))
	return platform.WriteLogBundle(w, s.app.Paths.LogDir, map[string][]byte{"diagnostics.json": diag, "config.json": cfg})
}

// ---- WebSockets ----------------------------------------------------------------------

func (s *Server) accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{strings.TrimPrefix(s.origin, "http://")}})
}

func (s *Server) wsEvents(w http.ResponseWriter, r *http.Request) {
	c, err := s.accept(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	ch := s.app.Hub.Subscribe()
	defer s.app.Hub.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) wsDisplay(w http.ResponseWriter, r *http.Request) {
	c, err := s.accept(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub := s.app.Streamer.Subscribe()
	defer s.app.Streamer.Unsubscribe(sub)
	go func() {
		defer cancel()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var in display.Input
			if json.Unmarshal(data, &in) == nil {
				s.app.Streamer.Send(in)
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-sub.C:
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageBinary, msg)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}
