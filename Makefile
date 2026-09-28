# droidpector build entry points. See BUILD.md.
#
#   make test          unit + integration tests (no VM)
#   make lint          go vet, staticcheck, gofmt, UI typecheck/lint
#   make ui            build the web UI (embedded into the exe)
#   make windows       Release build: build/droidpector.exe (windows/amd64)
#   make windows-debug Debug build:   build/debug/droidpector.exe
#   make runtime       prepare build/runtime (QEMU + Android runtime, pinned)
#   make portable      build/droidpector-portable-x64.zip (extract and run; primary deliverable)
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

.PHONY: portable all test test-race lint ui windows windows-debug runtime stage installer testapp test-vm e2e clean fmt

all: lint test windows

fmt:
	gofmt -w cmd src tests tools

test:
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

windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS) -s -w -H windowsgui" -o $(BUILD)/droidpector.exe ./cmd/droidpector

windows-debug:
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

portable: windows
	rm -rf $(BUILD)/portable && mkdir -p $(PORTABLE)/runtime/android/x86_64
	cp $(BUILD)/droidpector.exe src/portable/README.txt $(PORTABLE)/
	cp -R $(BUILD)/runtime/qemu $(BUILD)/runtime/licenses $(PORTABLE)/runtime/
	cp $(BUILD)/runtime/android/x86_64/kernel $(BUILD)/runtime/android/x86_64/initrd.img \
	   $(BUILD)/runtime/android/x86_64/data-template.qcow2 $(BUILD)/runtime/android/x86_64/runtime.json \
	   $(BUILD)/runtime/android/x86_64/system.iso $(PORTABLE)/runtime/android/x86_64/
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
