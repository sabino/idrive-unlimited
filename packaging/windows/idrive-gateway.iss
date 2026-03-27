#define AppName "idrive-gateway"
#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceDir
  #error "SourceDir must be defined"
#endif
#ifndef OutputDir
  #error "OutputDir must be defined"
#endif
#ifndef OutputBaseFilename
  #define OutputBaseFilename "idrive-gateway-setup"
#endif

[Setup]
AppId={{4A6524DC-17A1-4B4E-84AE-16D16E32E6EF}
AppName={#AppName}
AppVersion={#AppVersion}
AppPublisher=Sabino
DefaultDirName={localappdata}\Programs\idrive-gateway
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir={#OutputDir}
OutputBaseFilename={#OutputBaseFilename}
Compression=lzma
SolidCompression=yes
WizardStyle=modern
ChangesEnvironment=yes
UninstallDisplayIcon={app}\idrive-gateway.exe

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
Source: "{#SourceDir}\idrive-gateway.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\libstdc++-6.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\libgcc_s_seh-1.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\libwinpthread-1.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\MicrosoftEdgeWebView2Setup.exe"; Flags: dontcopy

[Code]
const
  WebView2ClientGUID = '{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}';

function QueryStringValue(RootKey: Integer; const Subkey, ValueName: string; var Value: string): Boolean;
begin
  Result := RegQueryStringValue(RootKey, Subkey, ValueName, Value) and (Trim(Value) <> '');
end;

function HasWebView2Runtime(): Boolean;
var
  Value: string;
begin
  Result :=
    QueryStringValue(HKLM, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\' + WebView2ClientGUID, 'pv', Value) or
    QueryStringValue(HKLM64, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\' + WebView2ClientGUID, 'pv', Value) or
    QueryStringValue(HKLM32, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\' + WebView2ClientGUID, 'pv', Value) or
    QueryStringValue(HKCU, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\' + WebView2ClientGUID, 'pv', Value);
end;

function EnsureWebView2Runtime(): Boolean;
var
  ResultCode: Integer;
  Bootstrapper: string;
begin
  Result := True;
  if HasWebView2Runtime() then
    exit;

  ExtractTemporaryFile('MicrosoftEdgeWebView2Setup.exe');
  Bootstrapper := ExpandConstant('{tmp}\MicrosoftEdgeWebView2Setup.exe');
  if not Exec(Bootstrapper, '/silent /install', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
  begin
    MsgBox('Failed to start the Microsoft Edge WebView2 Runtime bootstrapper. Install WebView2 manually and run the installer again.', mbCriticalError, MB_OK);
    Result := False;
    exit;
  end;

  if not HasWebView2Runtime() then
  begin
    MsgBox('Microsoft Edge WebView2 Runtime is still missing after the bootstrapper finished. Install it manually and run the installer again.', mbCriticalError, MB_OK);
    Result := False;
  end;
end;

function PathContains(PathValue, Dir: string): Boolean;
var
  SearchValue: string;
begin
  SearchValue := ';' + Lowercase(PathValue) + ';';
  Result := Pos(';' + Lowercase(Dir) + ';', SearchValue) > 0;
end;

procedure AddInstallDirToUserPath();
var
  PathValue: string;
  InstallDir: string;
begin
  InstallDir := ExpandConstant('{app}');
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', PathValue) then
    PathValue := '';

  if PathContains(PathValue, InstallDir) then
    exit;

  if (PathValue <> '') and (Copy(PathValue, Length(PathValue), 1) <> ';') then
    PathValue := PathValue + ';';
  PathValue := PathValue + InstallDir;
  RegWriteExpandStringValue(HKCU, 'Environment', 'Path', PathValue);
end;

procedure RemoveInstallDirFromUserPath();
var
  PathValue: string;
  InstallDir: string;
  Remaining: string;
  SeparatorPos: Integer;
  Part: string;
  Updated: string;
begin
  InstallDir := Lowercase(ExpandConstant('{app}'));
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', PathValue) then
    exit;

  Remaining := PathValue;
  Updated := '';
  while Remaining <> '' do
  begin
    SeparatorPos := Pos(';', Remaining);
    if SeparatorPos > 0 then
    begin
      Part := Copy(Remaining, 1, SeparatorPos - 1);
      Delete(Remaining, 1, SeparatorPos);
    end
    else
    begin
      Part := Remaining;
      Remaining := '';
    end;

    Part := Trim(Part);
    if (Part <> '') and (Lowercase(Part) <> InstallDir) then
    begin
      if Updated <> '' then
        Updated := Updated + ';';
      Updated := Updated + Part;
    end;
  end;

  RegWriteExpandStringValue(HKCU, 'Environment', 'Path', Updated);
end;

function InitializeSetup(): Boolean;
begin
  Result := EnsureWebView2Runtime();
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    AddInstallDirToUserPath();
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usPostUninstall then
    RemoveInstallDirFromUserPath();
end;
