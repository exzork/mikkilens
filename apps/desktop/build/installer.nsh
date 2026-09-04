; Uninstall behaviour for MikkiLens.
;
; electron-builder's own uninstaller removes the installed files and the
; registry keys it wrote, and stops the settings window. It does not know about
; the three things this file handles, all of which it would otherwise leave
; behind on the machine:
;
;   the engine       mikkilensd.exe is a child process the app started, not
;                    something NSIS installed. Left running, it holds
;                    resources\mikkilensd.exe open and the uninstall fails
;                    partway through with files it could not delete.
;
;   autostart        the settings window writes a Run entry when she turns
;                    "start with Windows" on. Left behind, every login tries to
;                    launch an executable that is no longer there -- an error
;                    at sign-in, on the machine she just tidied up.
;
;   her data         config.toml, the YouTube sign-in and the speech models
;                    live in %APPDATA%\MikkiLens. Those models are hundreds of
;                    megabytes to several gigabytes and are slow to fetch
;                    again, so they are kept unless she says otherwise.
;                    deleteAppDataOnUninstall stays false for that reason; this
;                    asks instead, which is the part it cannot express.

; Everything here is uninstaller-only, and this file is compiled twice: once
; for the installer and once for the uninstaller. Left ungated, the un.
; function makes makensis warn that uninstaller code was found with no
; uninstaller to put it in, and the variables warn that they are never used --
; and electron-builder treats every makensis warning as an error.
!ifdef BUILD_UNINSTALLER

Var /GLOBAL runKey
Var /GLOBAL runName
Var /GLOBAL runData
Var /GLOBAL runIndex
Var /GLOBAL pathLength
Var /GLOBAL pathHead
Var /GLOBAL runPrefix
Var /GLOBAL sepChar
Var /GLOBAL silentAsked
Var /GLOBAL scanAt
Var /GLOBAL scanPair

;
; Whether a silent uninstall was actually asked for.
;
; ${Silent} cannot answer this. A one-click uninstaller turns silent mode on
; itself, before either custom macro runs, so by the time we are called an
; ordinary uninstall from Add or Remove Programs is indistinguishable from one
; started with /S -- and prompting is exactly what separates them. The command
; line still tells the truth, so it is read directly. Windows paths are
; separated by backslashes, so "/S" does not turn up in one by accident.
;
; The answer goes in a global rather than through the stack: this is called
; once, and the register juggling a stack return needs is easy to get subtly
; wrong and hard to notice in an installer nobody runs twice.
;
Function un.askedForSilent
  StrCpy $silentAsked "0"
  StrCpy $scanAt 0

  scan:
    StrCpy $scanPair $CMDLINE 2 $scanAt
    StrCmp $scanPair "" scanDone
    StrCmp $scanPair "/S" quiet
    StrCmp $scanPair "/s" quiet
    IntOp $scanAt $scanAt + 1
    Goto scan

  quiet:
    StrCpy $silentAsked "1"

  scanDone:
FunctionEnd

;
; Stop the engine before anything is deleted.
;
; /T takes its children with it: whisper-server.exe is started by the engine
; and holds the speech model open, and killing only the parent leaves it as an
; orphan still holding a file the uninstaller wants to remove.
;
; Failure is ignored on purpose. The usual reason is that the engine was not
; running, which is not a problem, and a message about it would not help
; somebody who is uninstalling anyway.
;
!macro customUnInit
  DetailPrint "Stopping the MikkiLens engine..."
  nsExec::Exec 'taskkill /F /T /IM mikkilensd.exe'
  Pop $0
  nsExec::Exec 'taskkill /F /T /IM whisper-server.exe'
  Pop $0
  Sleep 500
!macroend

!macro customUnInstall
  ;
  ; Autostart, matched by where it points rather than by what it is called.
  ;
  ; Electron names the value after the application, and that name has changed
  ; between builds before. What cannot drift is the path: a Run entry that
  ; launches something out of this installation belongs to this installation,
  ; and is about to be pointing at nothing.
  ;
  ; The trailing separator matters. Matching on the bare install path also
  ; matches a sibling folder whose name merely starts with it -- a
  ; MikkiLensOther next to a MikkiLens -- and would switch off the autostart of
  ; a program this uninstaller has nothing to do with.
  ; The separator is taken as the first character of a two-character string
  ; rather than written on its own. NSIS reads a backslash at the end of a line
  ; as a line continuation, and one just before a closing quote as escaping
  ; that quote -- either way the statement runs into the next one and the error
  ; is about argument counts, a long way from the cause.
  StrCpy $runKey "Software\Microsoft\Windows\CurrentVersion\Run"
  StrCpy $sepChar "\ " 1
  StrCpy $runPrefix "$INSTDIR$sepChar"
  StrLen $pathLength $runPrefix

  ; Equal or longer carries on; anything shorter bails out. A short or empty
  ; $INSTDIR would compare nothing against nothing, match every value in the
  ; key, and delete the startup entry of every program on the machine. It
  ; should never happen -- the uninstaller runs from inside the installation --
  ; but the cost of being wrong is somebody else's software silently not
  ; starting, so it is checked.
  IntCmp $pathLength 4 0 autostartDone 0
  StrCpy $runIndex 0

  autostart:
    EnumRegValue $runName HKCU "$runKey" $runIndex
    StrCmp $runName "" autostartDone
    ReadRegStr $runData HKCU "$runKey" "$runName"

    ; Unquoted first, then past a leading quote. Electron writes the quoted
    ; form; a value written by hand or by an older build may not be.
    StrCpy $pathHead $runData $pathLength
    StrCmp $pathHead $runPrefix autostartHit
    StrCpy $pathHead $runData $pathLength 1
    StrCmp $pathHead $runPrefix autostartHit

    IntOp $runIndex $runIndex + 1
    Goto autostart

  autostartHit:
    DetailPrint "Removing $runName from startup..."
    DeleteRegValue HKCU "$runKey" "$runName"
    ; The list closed up behind that one, so the index stays where it is.
    Goto autostart

  autostartDone:

  ;
  ; The update cache: downloaded files with nothing of hers in them, so it goes
  ; without asking. Leaving it would keep a half-fetched update for a program
  ; that is no longer installed.
  ;
  RMDir /r "$LOCALAPPDATA\@mikkilensdesktop-updater"

  ;
  ; Her settings and her models. Kept by default: reinstalling on top of them
  ; is how she gets back to a working MikkiLens without waiting for gigabytes
  ; to come down again, and an uninstaller that quietly threw away a YouTube
  ; sign-in and a tuned config.toml would be a nasty surprise.
  ;
  ; /SD IDNO makes the unattended answer "keep", so `/S` never deletes her data.
  ;
  IfFileExists "$APPDATA\MikkiLens\*.*" 0 dataDone
    Call un.askedForSilent
    StrCmp $silentAsked "1" dataDone

    MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 \
      "Also remove your MikkiLens settings and downloaded speech models?$\r$\n$\r$\n\
This deletes your settings, your YouTube sign-in and the voice models in:$\r$\n\
$APPDATA\MikkiLens$\r$\n$\r$\n\
Choose No to keep them, so reinstalling MikkiLens does not have to download them again." \
      /SD IDNO IDYES dataRemove

    Goto dataDone

  dataRemove:
    DetailPrint "Removing MikkiLens settings and models..."
    RMDir /r "$APPDATA\MikkiLens"

  dataDone:

  ;
  ; Finally, the installation folder itself.
  ;
  ; The uninstaller starts with SetOutPath $INSTDIR, which makes the folder its
  ; working directory -- and Windows will not remove a directory that is some
  ; running process's working directory. So the template's own RMDir quietly
  ; fails and an empty MikkiLens folder is left sitting in Programs, looking
  ; like an uninstall that did not finish. Stepping out of it first is all it
  ; takes. RMDir without /r, so this can only ever remove an empty folder: if
  ; anything of hers is still in there, it stays.
  ;
  SetOutPath $TEMP
  RMDir "$INSTDIR"
!macroend

!endif
