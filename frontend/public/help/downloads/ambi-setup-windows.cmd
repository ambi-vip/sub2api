@echo off
setlocal EnableExtensions DisableDelayedExpansion
set "AMBI_SETUP_SCRIPT=%~f0"
set "AMBI_POWERSHELL=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if exist "%SystemRoot%\Sysnative\WindowsPowerShell\v1.0\powershell.exe" set "AMBI_POWERSHELL=%SystemRoot%\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%AMBI_POWERSHELL%" goto :missing_powershell
"%AMBI_POWERSHELL%" -NoLogo -NoProfile -STA -ExecutionPolicy Bypass -Command "try { $source = [IO.File]::ReadAllText($env:AMBI_SETUP_SCRIPT, [Text.Encoding]::UTF8); $marker = '# AMBI_POWERSHELL'; & ([ScriptBlock]::Create($source.Substring($source.LastIndexOf($marker) + $marker.Length))) } catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }"
set "AMBI_SETUP_EXIT=%ERRORLEVEL%"
if "%AMBI_SETUP_EXIT%"=="0" exit /b 0
echo Setup did not complete. Press any key to close.
pause >nul
exit /b %AMBI_SETUP_EXIT%

:missing_powershell
echo Windows PowerShell is unavailable. Enable it and run this file again.
pause
exit /b 1

# AMBI_POWERSHELL
$ErrorActionPreference = 'Stop'
$ambiBaseUrl = 'https://sub.ambi.vip/v1'
$ambiCatalogUrl = 'https://sub.ambi.vip/v1/models?client_version=0.158.0'
$ambiStoreId = '9PLM9XGG6VKS'
$ambiTempFile = ''
$ambiBackupFile = ''
$ambiConfigFile = ''
$ambiFailure = '配置未完成，请查看终端错误信息。'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
try { [Console]::OutputEncoding = [Text.Encoding]::UTF8 } catch { }

function Read-AmbiKey {
  $form = New-Object Windows.Forms.Form
  try {
    $form.Text = 'Ambi Sub2API 一键配置'
    $form.ClientSize = New-Object Drawing.Size(490, 260)
    $form.StartPosition = 'CenterScreen'
    $form.FormBorderStyle = 'FixedDialog'
    $form.MaximizeBox = $false
    $form.MinimizeBox = $false
    $label = New-Object Windows.Forms.Label
    $label.SetBounds(18, 15, 452, 145)
    $label.Text = "请输入 sub.ambi.vip 的 API Key。`r`n`r`n脚本会安装桌面应用，备份并重建 Codex 配置。密钥仅发送到 sub.ambi.vip 读取模型列表，并保存到仅当前用户可读的本机配置文件。`r`n`r`n缺少 winget 时，会请求系统授权并运行 asheroto.com 的第三方安装脚本。"
    $keyBox = New-Object Windows.Forms.TextBox
    $keyBox.SetBounds(18, 166, 452, 25)
    $keyBox.UseSystemPasswordChar = $true
    $ok = New-Object Windows.Forms.Button
    $ok.Text = '继续'
    $ok.SetBounds(290, 213, 85, 28)
    $ok.DialogResult = [Windows.Forms.DialogResult]::OK
    $cancel = New-Object Windows.Forms.Button
    $cancel.Text = '取消'
    $cancel.SetBounds(385, 213, 85, 28)
    $cancel.DialogResult = [Windows.Forms.DialogResult]::Cancel
    $form.Controls.AddRange(@($label, $keyBox, $ok, $cancel))
    $form.AcceptButton = $ok
    $form.CancelButton = $cancel
    $form.Add_Shown({ [void]$keyBox.Focus() })
    if ($form.ShowDialog() -ne [Windows.Forms.DialogResult]::OK) { throw '已取消配置。' }
    $key = $keyBox.Text.Trim()
    if ([string]::IsNullOrWhiteSpace($key) -or $key -match '[\x00-\x1f\x7f]') { throw 'API Key 为空或含非法字符。' }
    return $key
  } finally { $form.Dispose() }
}

function Select-AmbiModel([string]$apiKey) {
  try {
    $response = Invoke-RestMethod -Method Get -Uri $ambiCatalogUrl -TimeoutSec 45 -Headers @{ Authorization = "Bearer $apiKey" }
  } catch {
    throw '无法读取模型列表，请确认 API Key 权限、额度与网络连接。'
  }
  if (-not ($response.PSObject.Properties.Name -contains 'models') -or $null -eq $response.models) {
    throw '该 API Key 未返回 Codex 模型目录，请使用支持 Codex 的令牌。'
  }
  $models = @($response.models | ForEach-Object { $_.slug } | Where-Object {
    $_ -is [string] -and $_.Length -gt 0 -and $_ -notmatch '[\x00-\x1f\x7f]'
  } | Sort-Object -Unique)
  if ($models.Count -eq 0) { throw '该 API Key 没有可用模型。' }
  $form = New-Object Windows.Forms.Form
  try {
    $form.Text = '选择 Sub2API 模型'
    $form.ClientSize = New-Object Drawing.Size(490, 165)
    $form.StartPosition = 'CenterScreen'
    $form.FormBorderStyle = 'FixedDialog'
    $form.MaximizeBox = $false
    $form.MinimizeBox = $false
    $label = New-Object Windows.Forms.Label
    $label.SetBounds(18, 14, 452, 42)
    $label.Text = '请选择支持 Responses 接口的对话模型。这里只读取模型列表，不会发起付费对话。'
    $select = New-Object Windows.Forms.ComboBox
    $select.SetBounds(18, 64, 452, 26)
    $select.DropDownStyle = 'DropDownList'
    $select.Items.AddRange([object[]]$models)
    $preferred = @($models | Where-Object { $_ -match '^gpt-' -and $_ -notmatch 'image|audio|realtime' })
    $select.SelectedIndex = 0
    if ($preferred.Count -gt 0) { $select.SelectedItem = $preferred[0] }
    $ok = New-Object Windows.Forms.Button
    $ok.Text = '安装并配置'
    $ok.SetBounds(278, 116, 96, 28)
    $ok.DialogResult = [Windows.Forms.DialogResult]::OK
    $cancel = New-Object Windows.Forms.Button
    $cancel.Text = '取消'
    $cancel.SetBounds(385, 116, 85, 28)
    $cancel.DialogResult = [Windows.Forms.DialogResult]::Cancel
    $form.Controls.AddRange(@($label, $select, $ok, $cancel))
    $form.AcceptButton = $ok
    $form.CancelButton = $cancel
    if ($form.ShowDialog() -ne [Windows.Forms.DialogResult]::OK) { throw '已取消配置。' }
    return [string]$select.SelectedItem
  } finally { $form.Dispose() }
}

function Find-AmbiWinget {
  $command = Get-Command winget.exe -ErrorAction SilentlyContinue
  if ($command) { return $command.Source }
  $alias = Join-Path $env:LOCALAPPDATA 'Microsoft\WindowsApps\winget.exe'
  if (Test-Path -LiteralPath $alias -PathType Leaf) { return $alias }
  return $null
}

function Install-AmbiApp {
  $package = Get-AppxPackage -Name 'OpenAI.Codex' -ErrorAction SilentlyContinue
  if ($package) { return }
  $winget = Find-AmbiWinget
  if (-not $winget) {
    Write-Host '缺少 winget，正在请求系统授权以安装…'
    $bootstrap = "`$ErrorActionPreference = 'Stop'; [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; irm https://asheroto.com/winget | iex"
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($bootstrap))
    $process = Start-Process -FilePath (Join-Path $PSHOME 'powershell.exe') -Verb RunAs -Wait -PassThru -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-EncodedCommand', $encoded)
    if ($process.ExitCode -ne 0) { throw 'winget 安装未完成。请安装后重新运行脚本。' }
    # Only the installer is elevated. Configuration stays with the original user.
    $winget = Find-AmbiWinget
    if (-not $winget) { throw '未找到 winget，请重新登录 Windows 后重试。' }
  }
  Write-Host '先搜索 Microsoft Store 中的官方 ChatGPT…'
  $search = & $winget search ChatGPT --source msstore --accept-source-agreements --disable-interactivity 2>&1
  $searchExit = $LASTEXITCODE
  $search | ForEach-Object { Write-Host $_ }
  # Use only the official product ID, and require it to appear in search results.
  # Do not install a same-name third-party result automatically.
  if ($searchExit -ne 0 -or ($search -join "`n") -notmatch ('\b' + [regex]::Escape($ambiStoreId) + '\b')) {
    throw '搜索结果中没有可确认的官方 ChatGPT。请从 chatgpt.com/download 安装新版后重新运行脚本。'
  }
  & $winget install --id $ambiStoreId --exact --source msstore --accept-source-agreements --accept-package-agreements --disable-interactivity
  if ($LASTEXITCODE -ne 0) { throw 'ChatGPT 安装未完成，请检查终端信息并重试。' }
}

function ConvertTo-AmbiToml([string]$value) {
  if ($value -match '[\x00-\x1f\x7f]') { throw '配置值含非法控制字符。' }
  return $value.Replace('\', '\\').Replace('"', '\"')
}

function Set-AmbiPrivateFile([string]$path) {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
  $acl = New-Object Security.AccessControl.FileSecurity
  $acl.SetOwner($identity)
  $acl.SetAccessRuleProtection($true, $false)
  $rule = New-Object Security.AccessControl.FileSystemAccessRule($identity, 'FullControl', 'Allow')
  $acl.AddAccessRule($rule)
  Set-Acl -LiteralPath $path -AclObject $acl
}

function Write-AmbiConfig([string]$apiKey, [string]$model) {
  $configDir = $env:CODEX_HOME
  if ([string]::IsNullOrWhiteSpace($configDir)) {
    $configDir = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.codex'
  }
  $configDir = [IO.Path]::GetFullPath($configDir)
  [void][IO.Directory]::CreateDirectory($configDir)
  $script:ambiConfigFile = Join-Path $configDir 'config.toml'
  if (Test-Path -LiteralPath $ambiConfigFile) {
    $existing = Get-Item -Force -LiteralPath $ambiConfigFile
    if ($existing.PSIsContainer -or ($existing.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
      throw 'config.toml 是链接或非普通文件，请手动处理后重试。'
    }
  }
  $keyText = ConvertTo-AmbiToml $apiKey
  $modelText = ConvertTo-AmbiToml $model
  $config = @"
# Generated by the Ambi Sub2API desktop setup.
model_provider = "ambi_vip"
model = "$modelText"

[model_providers.ambi_vip]
name = "Ambi Sub2API"
base_url = "$ambiBaseUrl"
model_catalog_url = "$ambiCatalogUrl"
wire_api = "responses"
requires_openai_auth = false
experimental_bearer_token = "$keyText"
supports_websockets = false
"@
  $script:ambiTempFile = Join-Path $configDir ('.ambi-config.' + [Guid]::NewGuid().ToString('N'))
  # Restrict the empty file before any credentials are written to it.
  [IO.File]::WriteAllBytes($ambiTempFile, [byte[]]@())
  Set-AmbiPrivateFile $ambiTempFile
  [IO.File]::WriteAllBytes($ambiTempFile, [Text.Encoding]::UTF8.GetBytes($config + "`n"))
  if ([IO.File]::Exists($ambiConfigFile)) {
    $script:ambiBackupFile = $ambiConfigFile + '.ambi-backup.' + [Guid]::NewGuid().ToString('N')
    [IO.File]::WriteAllBytes($ambiBackupFile, [byte[]]@())
    Set-AmbiPrivateFile $ambiBackupFile
    [IO.File]::WriteAllBytes($ambiBackupFile, [IO.File]::ReadAllBytes($ambiConfigFile))
    Set-AmbiPrivateFile $ambiConfigFile
    [IO.File]::Replace($ambiTempFile, $ambiConfigFile, [System.Management.Automation.Language.NullString]::Value)
  } else {
    [IO.File]::Move($ambiTempFile, $ambiConfigFile)
  }
  $script:ambiTempFile = ''
}

$ambiStatus = 1
try {
  Add-Type -AssemblyName System.Windows.Forms
  Add-Type -AssemblyName System.Drawing
  [Windows.Forms.Application]::EnableVisualStyles()
  $ambiFailure = '已取消或未填写有效的 API Key，原配置没有更改。'
  $key = Read-AmbiKey
  $ambiFailure = '模型选择未完成。请检查 API Key 权限、额度和网络连接。原配置没有更改。'
  $model = Select-AmbiModel $key
  $ambiFailure = '应用安装未完成，请检查终端信息，或从 chatgpt.com/download 安装新版桌面应用后重新运行脚本。原配置没有更改。'
  Install-AmbiApp
  $ambiFailure = '配置写入未完成，请检查目录权限和磁盘空间。原配置备份位于 config.toml 同目录。'
  Write-AmbiConfig $key $model
  $key = $null
  $ambiStatus = 0
  $message = "配置完成。`r`n`r`n中转地址：$ambiBaseUrl`r`n配置文件：$ambiConfigFile"
  if ($ambiBackupFile) { $message += "`r`n原配置备份：$ambiBackupFile" }
  $message += "`r`n`r`n请使用桌面应用中的 Codex 本地模式。若应用已打开，请先完全退出再重开。Windows 请使用原生模式；WSL 配置需要另行设置。ChatGPT 其他功能仍使用各自的账号登录。"
  try {
    $app = Get-StartApps | Where-Object { $_.AppID -like 'OpenAI.Codex_*' } | Select-Object -First 1
    if ($app) { Start-Process explorer.exe -ArgumentList ('shell:AppsFolder\' + $app.AppID) }
  } catch { Write-Host '配置已写入，请从开始菜单打开 ChatGPT。' }
  Write-Host $message
  [void][Windows.Forms.MessageBox]::Show($message, 'Ambi Sub2API 配置成功', 'OK', 'Information')
} catch {
  # Never echo server response bodies or credentials into error dialogs/logs.
  Write-Host $ambiFailure
  try { [void][Windows.Forms.MessageBox]::Show($ambiFailure, 'Ambi Sub2API', 'OK', 'Error') } catch { }
} finally {
  $key = $null
  if ($ambiTempFile -and [IO.File]::Exists($ambiTempFile)) { [IO.File]::Delete($ambiTempFile) }
}
exit $ambiStatus
