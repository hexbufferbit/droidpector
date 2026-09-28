# droidpector build entry points. See BUILD.md.
#
#   make test          unit + integration tests (no VM)
#   make lint          go vet, staticcheck, gofmt, UI typecheck/lint
#   make ui            build the web UI (embedded into the exe)
#   make windows       Release build: build/droidpector.exe (windows/amd64)
#   make windows-debug Debug build:   build/debug/droidpector.exe
#   make runtime       prepare build/runtime (QEMU + Android runtime, pinned)
#   make portable      build/droidpector-portable-x64.zip (extract and run; primary deliverable)
#   make sign          Authenticode-sign build/droidpector.exe (SIGN_PFX=cert.pfx SIGN_PASS=…)
#   make installer     build/droidpector-Setup-x64.exe (NSIS, optional)
#   make testapp       build/testapp/TestApp.apk
#   make test-vm       real-VM integration tests (needs runtime + TestApp)
#   make e2e           UI end-to-end tests (Playwright, real capture, no VM)

GO        ?= go
NPM       ?= npm
MAKENSIS  ?= makensis
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo 0.0.0-dev)
NSIS_VERSION ?= $(shell echo $(VERSION) | grep -Eo '^[0-9]+\.[0-9]+\.[0-9]+' || echo 0.0.0)
BUILD     := build
STAGE     := $(BUILD)/stage
LDFLAGS   := -X main.version=$(VERSION)
UI_DIR    := src/ui/web
WEBVIEW2_BOOTSTRAPPER_URL := https://go.microsoft.com/fwlink/p/?LinkId=2124703

.PHONY: agent sign portable all test test-race lint ui windows windows-debug runtime stage installer testapp test-vm e2e clean fmt

all: lint test windows

fmt:
	gofmt -w cmd src tests tools

test: agent
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -race -count=1 ./...

lint:
	@test -z "$$(gofmt -l cmd src tests tools)" || (echo "gofmt needed:"; gofmt -l cmd src tests tools; exit 1)
	$(GO) vet ./...
	GOOS=windows GOARCH=amd64 $(GO) vet ./...
	$(GO) vet -tags vm ./tests/vm/
	$(GO) run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
	cd $(UI_DIR) && $(NPM) run typecheck && $(NPM) run lint

ui:
	cd $(UI_DIR) && $(NPM) ci && $(NPM) run build

# In-guest touch agent (static linux/amd64), embedded into the Windows exe.
AGENT := src/android/agent/bin/droidpector-agent-linux-amd64
agent:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(AGENT) ./cmd/droidpector-agent

windows: agent
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS) -H windowsgui" -o $(BUILD)/droidpector.exe ./cmd/droidpector

windows-debug: agent
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -gcflags "all=-N -l" -ldflags "$(LDFLAGS)" -o $(BUILD)/debug/droidpector.exe ./cmd/droidpector

runtime:
	./tools/runtime/prepare.sh $(BUILD)/runtime

stage: windows
	rm -rf $(STAGE) && mkdir -p $(STAGE)/runtime/android/x86_64
	cp $(BUILD)/droidpector.exe $(STAGE)/
	cp -R $(BUILD)/runtime/qemu $(BUILD)/runtime/licenses $(STAGE)/runtime/
	cp $(BUILD)/runtime/android/x86_64/kernel $(BUILD)/runtime/android/x86_64/initrd.img \
	   $(BUILD)/runtime/android/x86_64/data-template.qcow2 $(BUILD)/runtime/android/x86_64/runtime.json \
	   $(BUILD)/runtime/android/x86_64/system.iso $(STAGE)/runtime/android/x86_64/
	test -f $(BUILD)/MicrosoftEdgeWebview2Setup.exe || curl -fsSL -o $(BUILD)/MicrosoftEdgeWebview2Setup.exe "$(WEBVIEW2_BOOTSTRAPPER_URL)"
	cp $(BUILD)/MicrosoftEdgeWebview2Setup.exe $(STAGE)/

PORTABLE := $(BUILD)/portable/droidpector

# Code signing (optional, but the only reliable cure for antivirus false
# positives on an unsigned Go executable). Works on any OS with osslsigncode;
# on Windows, signtool from the Windows SDK can be used instead.
SIGN_PFX  ?=
SIGN_PASS ?=
SIGN_TS   ?= http://timestamp.digicert.com
sign:
	@test -n "$(SIGN_PFX)" || (echo "usage: make sign SIGN_PFX=cert.pfx SIGN_PASS=password"; exit 2)
	osslsigncode sign -pkcs12 "$(SIGN_PFX)" -pass "$(SIGN_PASS)" -n "droidpector" -i https://github.com/droidpector \
	  -t "$(SIGN_TS)" -h sha256 -in $(BUILD)/droidpector.exe -out $(BUILD)/droidpector-signed.exe
	mv $(BUILD)/droidpector-signed.exe $(BUILD)/droidpector.exe
	osslsigncode verify -in $(BUILD)/droidpector.exe

portable: windows
	rm -rf $(BUILD)/portable && mkdir -p $(PORTABLE)/runtime/android/x86_64
	cp $(BUILD)/droidpector.exe src/portable/README.txt $(PORTABLE)/
	cp -R $(BUILD)/runtime/qemu $(BUILD)/runtime/licenses $(PORTABLE)/runtime/
	cp $(BUILD)/runtime/android/x86_64/kernel $(BUILD)/runtime/android/x86_64/initrd.img \
	   $(BUILD)/runtime/android/x86_64/data-template.qcow2 $(BUILD)/runtime/android/x86_64/runtime.json \
	   $(BUILD)/runtime/android/x86_64/system.iso $(PORTABLE)/runtime/android/x86_64/
	cd $(PORTABLE) && (command -v sha256sum >/dev/null && sha256sum droidpector.exe runtime/qemu/*.exe || shasum -a 256 droidpector.exe runtime/qemu/*.exe) > CHECKSUMS.txt
	$(GO) run ./tools/portablezip -src $(PORTABLE) -out $(BUILD)/droidpector-portable-x64.zip
	@ls -la $(BUILD)/droidpector-portable-x64.zip

installer: stage
	$(MAKENSIS) -V2 -DVERSION=$(NSIS_VERSION) -DSTAGE=$(abspath $(STAGE)) -DOUTFILE=$(abspath $(BUILD))/droidpector-Setup-x64.exe src/installer/installer.nsi
	@ls -la $(BUILD)/*.exe

testapp:
	./tools/testapp/build.sh

test-vm: testapp
	APKINSPECTOR_RUNTIME_DIR=$(abspath $(BUILD))/runtime APKINSPECTOR_TESTAPP=$(abspath $(BUILD))/testapp/TestApp.apk \
	  $(GO) test -tags vm -timeout 120m -count=1 -v ./tests/vm/

e2e:
	cd $(UI_DIR) && $(NPM) run e2e

clean:
	rm -rf $(BUILD)
