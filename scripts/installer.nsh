; Keep this list in sync with the files bundled in the Windows installer.
!macro customUnInstall
  SetOutPath "$TEMP"
  Delete "$INSTDIR\Xiaomu.exe"
  Delete "$INSTDIR\mpv-2.dll"
  Delete "$INSTDIR\THIRD_PARTY_NOTICES.txt"
  Delete "$INSTDIR\icon.png"
  Delete "$INSTDIR\.mygo-files"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  ; MyGo has already removed the shortcuts and registry entries. Leave the
  ; uninstall section before its default recursive directory removal.
  Return
!macroend
