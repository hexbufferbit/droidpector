; droidpector - Windows x64 installer (NSIS 3, Unicode, MUI2).
;
; Build (from repo root, after `make windows runtime`):
;   makensis -DVERSION=1.0.0 -DSTAGE=build\stage -DOUTFILE=build\droidpector-Setup-x64.exe src\installer\installer.nsi
;
; STAGE layout:
;   droidpector.exe
;   runtime\qemu\...                  (pruned QEMU for Windows)
;   runtime\android\x86_64\{kernel,initrd.img,data-template.qcow2,runtime.json}
;   runtime\licenses\...
;   runtime\android\x86_64\system.iso    (Android system image, xz squashfs, ~1.6 GB)
;   MicrosoftEdgeWebview2Setup.exe    (Evergreen bootstrapper, redistributable)
;
; Everything, including the Android system image, is embedded: the
; installer works offline. The image is repacked losslessly (see
; tools/runtime/prepare.sh) so the whole payload stays below NSIS's 2 GB limit.

Unicode true
ManifestDPIAware true
SetCompressor lzma
SetCompressorDictSize 64
RequestExecutionLevel admin

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef STAGE
  !define STAGE "..\..\build\stage"
!endif
!ifndef OUTFILE
  !define OUTFILE "..\..\build\droidpector-Setup-x64.exe"
!endif
!define APPNAME "droidpector"
!define DISPLAYNAME "droidpector"
!define PUBLISHER "droidpector Project"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}"
!define WEBVIEW2_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

!include "MUI2.nsh"
!include "x64.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "WinVer.nsh"

Name "${DISPLAYNAME}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\${APPNAME}"
InstallDirRegKey HKLM "${UNINSTKEY}" "InstallLocation"
BrandingText "${DISPLAYNAME} ${VERSION}"
VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${DISPLAYNAME}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "CompanyName" "${PUBLISHER}"
VIAddVersionKey "FileDescription" "${DISPLAYNAME} Setup"
VIAddVersionKey "LegalCopyright" "See licenses folder"

!define MUI_ABORTWARNING
!define MUI_COMPONENTSPAGE_SMALLDESC
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${STAGE}\runtime\licenses\NOTICE.txt"
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\droidpector.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Launch ${DISPLAYNAME}"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Var RemoveUserData

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_OK|MB_ICONSTOP "${DISPLAYNAME} requires 64-bit Windows 10 or Windows 11."
    Abort
  ${EndIf}
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_OK|MB_ICONSTOP "${DISPLAYNAME} requires Windows 10 or Windows 11."
    Abort
  ${EndIf}
  SetRegView 64
FunctionEnd

!macro StopRunning
  ; Close a running instance (and its VM) before files are replaced.
  nsExec::Exec 'taskkill /F /T /IM droidpector.exe'
  Pop $0
  Sleep 500
!macroend

Section "${DISPLAYNAME} (required)" SecApp
  SectionIn RO
  !insertmacro StopRunning
  SetOutPath "$INSTDIR"
  File "${STAGE}\droidpector.exe"
  SetOutPath "$INSTDIR\runtime"
  File /r "${STAGE}\runtime\qemu"
  File /r "${STAGE}\runtime\licenses"
  SetOutPath "$INSTDIR\runtime\android\x86_64"
  File "${STAGE}\runtime\android\x86_64\kernel"
  File "${STAGE}\runtime\android\x86_64\initrd.img"
  File "${STAGE}\runtime\android\x86_64\data-template.qcow2"
  File "${STAGE}\runtime\android\x86_64\runtime.json"

  ; Microsoft Edge WebView2 Runtime (preinstalled on Windows 11 / updated Windows 10).
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ReadRegStr $1 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${If} $0 == ""
  ${AndIf} $1 == ""
    DetailPrint "Installing the Microsoft Edge WebView2 Runtime..."
    SetOutPath "$PLUGINSDIR"
    File "${STAGE}\MicrosoftEdgeWebview2Setup.exe"
    ExecWait '"$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install' $2
    ${If} $2 != 0
      MessageBox MB_OK|MB_ICONEXCLAMATION "The Microsoft Edge WebView2 Runtime could not be installed (code $2). ${DISPLAYNAME} needs it to display its window; install it from https://go.microsoft.com/fwlink/p/?LinkId=2124703"
    ${EndIf}
  ${EndIf}

  ; Uninstaller + Add/Remove Programs entry.
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${DISPLAYNAME}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKLM "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\droidpector.exe"
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKLM "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1

  CreateDirectory "$SMPROGRAMS\${DISPLAYNAME}"
  CreateShortcut "$SMPROGRAMS\${DISPLAYNAME}\${DISPLAYNAME}.lnk" "$INSTDIR\droidpector.exe"
  CreateShortcut "$SMPROGRAMS\${DISPLAYNAME}\Uninstall ${DISPLAYNAME}.lnk" "$INSTDIR\Uninstall.exe"
SectionEnd

Section "Android 13 runtime (required)" SecAndroid
  SectionIn RO
  SetOutPath "$INSTDIR\runtime\android\x86_64"
  ; Already xz-compressed: store it as-is instead of spending minutes on LZMA.
  SetCompress off
  File "${STAGE}\runtime\android\x86_64\system.iso"
  SetCompress auto
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKLM "${UNINSTKEY}" "EstimatedSize" $0
SectionEnd

Section "Desktop shortcut" SecDesktop
  CreateShortcut "$DESKTOP\${DISPLAYNAME}.lnk" "$INSTDIR\droidpector.exe"
SectionEnd

Section "Enable Windows Hypervisor Platform (recommended)" SecWHPX
  ; Hardware acceleration makes Android run at native speed. Changing a
  ; Windows feature requires the user's consent (this checkbox) and a restart.
  DetailPrint "Enabling Windows Hypervisor Platform..."
  nsExec::ExecToLog 'dism.exe /Online /Enable-Feature /FeatureName:HypervisorPlatform /All /NoRestart'
  Pop $0
  ${If} $0 == 3010
    SetRebootFlag true
  ${ElseIf} $0 != 0
    DetailPrint "Windows Hypervisor Platform could not be enabled (code $0); Android will run in software emulation."
  ${EndIf}
SectionEnd

LangString DESC_App ${LANG_ENGLISH} "The application, its virtualization engine (QEMU) and the Microsoft Edge WebView2 Runtime if missing."
LangString DESC_Android ${LANG_ENGLISH} "Android 13 (x86_64 with ARM app translation), about 1.6 GB. Included in this installer; no download needed."
LangString DESC_Desktop ${LANG_ENGLISH} "Create a desktop shortcut."
LangString DESC_WHPX ${LANG_ENGLISH} "Turns on the Windows feature 'Windows Hypervisor Platform' for hardware-accelerated Android. Requires a restart."
!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecApp} $(DESC_App)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecAndroid} $(DESC_Android)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} $(DESC_Desktop)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecWHPX} $(DESC_WHPX)
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Function un.onInit
  SetRegView 64
  StrCpy $RemoveUserData "0"
  ${GetParameters} $0
  ${GetOptions} $0 "/REMOVEDATA" $1
  ${IfNot} ${Errors}
    StrCpy $RemoveUserData "1"
  ${EndIf}
  IfSilent done
  MessageBox MB_YESNO|MB_ICONQUESTION "Also delete your sandbox (installed apps, snapshots) and captured sessions?$\r$\n$\r$\nChoose No to keep them for a future installation." IDNO done
  StrCpy $RemoveUserData "1"
  done:
FunctionEnd

Section "Uninstall"
  !insertmacro StopRunning
  Delete "$INSTDIR\droidpector.exe"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir /r "$INSTDIR\runtime"
  RMDir "$INSTDIR"
  Delete "$DESKTOP\${DISPLAYNAME}.lnk"
  RMDir /r "$SMPROGRAMS\${DISPLAYNAME}"
  DeleteRegKey HKLM "${UNINSTKEY}"
  ${If} $RemoveUserData == "1"
    SetShellVarContext current
    RMDir /r "$LOCALAPPDATA\${APPNAME}"
  ${EndIf}
SectionEnd
