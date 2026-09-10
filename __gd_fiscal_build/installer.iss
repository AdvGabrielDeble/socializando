#define MyAppName "GD Soluções — Fiscal Saúde"
#define MyAppVersion "1.0.0-mvp"
#define MyAppPublisher "Gabriel Deble"
#define MyAppExeName "GD-Fiscal-Saude-MVP-FINAL.exe"

[Setup]
AppId={{A8B71FA2-AD8D-4C88-AE73-0EA6D2A1C831}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={localappdata}\Programs\GD Solucoes\Fiscal Saude
DefaultGroupName=GD Soluções
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=installer_output
OutputBaseFilename=GD-Solucoes-Fiscal-Saude-MVP-FINAL
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\{#MyAppExeName}
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible

[Files]
Source: "dist\GD-Fiscal-Saude-MVP-FINAL.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\GD Soluções\GD Fiscal Saúde"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\GD Fiscal Saúde"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Criar atalho na área de trabalho"; GroupDescription: "Atalhos:"; Flags: unchecked

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "Abrir GD Fiscal Saúde"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
; Dados fiscais ficam deliberadamente fora da pasta do programa, em %LOCALAPPDATA%\GD Solucoes\Fiscal Saude.
; A desinstalação não remove o SQLite nem as exportações do usuário.
