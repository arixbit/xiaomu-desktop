; Installer hooks for xiaomu (windows.installerInclude in mygo.json).
;
; The uninstaller must only remove files this installer put in $INSTDIR.
; MyGo's generated uninstall section ends with RMDir /r "$INSTDIR", which
; would also delete files the user put in the folder themselves: the
; installer lets the user pick any folder, including one they already use,
; and anything added there after the install. customUnInstall runs before
; that line, so it removes the installed files one by one and then points
; $INSTDIR at a path that does not exist, so the recursive remove that
; follows deletes nothing.
;
; The list of installed files is the one the installer itself wrote into
; $INSTDIR\.mygo-files at build time (MyGo writes it next to the app: the
; executable and the top-level resources, one name per line). It does not
; name itself or Uninstall.exe, so those are deleted explicitly below.
; Entries that are directories are removed with plain RMDir, which only
; removes empty directories, so files the user added keep the folder
; (and themselves) in place.

!macro customUnInstall
  ; Delete every entry the installed version recorded in .mygo-files.
  ; If the file is missing (an older install, or the user removed it),
  ; nothing is deleted here and the explicit deletes below still run.
  ClearErrors
  FileOpen $0 "$INSTDIR\.mygo-files" r
  IfErrors xiaomu_uninstall_no_list
  xiaomu_uninstall_read_loop:
    ClearErrors
    FileRead $0 $1
    IfErrors xiaomu_uninstall_close_list
    Delete "$INSTDIR\$1"
    RMDir "$INSTDIR\$1"
    Goto xiaomu_uninstall_read_loop
  xiaomu_uninstall_close_list:
  FileClose $0
  xiaomu_uninstall_no_list:
  ; The list does not name itself, and the uninstaller is not in it
  ; either (it runs from a copy of itself, as MyGo's generated section
  ; assumes, so deleting it here is safe).
  Delete "$INSTDIR\.mygo-files"
  Delete "$INSTDIR\Uninstall.exe"
  ; Remove the install folder, but only if it is empty now.
  RMDir "$INSTDIR"
  ; Neutralise the recursive remove MyGo's generated uninstall section
  ; runs next: it only sees this path, which does not exist.
  StrCpy $INSTDIR "$TEMP\XiaomuUninstall"
!macroend
