#!/bin/bash
# Ambi Sub2API desktop setup. No credentials are embedded in this file.
# Source this file to exercise individual functions without running setup.
set -euo pipefail

ambi_base_url='https://sub.ambi.vip/v1'
ambi_catalog_url='https://sub.ambi.vip/v1/models?client_version=0.158.0'
ambi_work_dir=''
ambi_config_temp=''
ambi_config_file=''
ambi_backup_file=''
ambi_error='安装未完成，请查看终端中的错误信息后重试。'

ambi_cleanup() {
  [ -z "$ambi_config_temp" ] || rm -f "$ambi_config_temp"
  [ -z "$ambi_work_dir" ] || rm -rf "$ambi_work_dir"
}

ambi_finish() {
  local status=$?
  trap - EXIT
  ambi_cleanup
  if [ "$status" -ne 0 ]; then
    printf '%s\n' "$ambi_error" >&2
    /usr/bin/osascript - "$ambi_error" <<'APPLESCRIPT' || :
on run argv
  display dialog (item 1 of argv) with title "Ambi Sub2API" buttons {"好"} default button 1 with icon stop
end run
APPLESCRIPT
  fi
  exit "$status"
}

ambi_read_key() {
  /usr/bin/osascript <<'APPLESCRIPT'
set answer to display dialog "请输入 sub.ambi.vip 的 API Key。\n\n脚本会安装 ChatGPT 桌面应用，备份并重建 Codex 的 config.toml，再配置中转。密钥只在本机输入，仅发送到 sub.ambi.vip 读取模型列表，并保存到仅当前用户可读的配置文件。" with title "Ambi Sub2API 一键配置" default answer "" with hidden answer buttons {"取消", "继续"} default button "继续" cancel button "取消"
return text returned of answer
APPLESCRIPT
}

ambi_fetch_models() {
  local api_key=$1 response_file=$2 escaped_key status
  # curl reads the header from stdin, so the key is not a process argument.
  escaped_key=${api_key//\\/\\\\}
  escaped_key=${escaped_key//\"/\\\"}
  status=$(printf 'header = "Authorization: Bearer %s"\n' "$escaped_key" |
    /usr/bin/curl --config - --proto '=https' --tlsv1.2 --silent --show-error \
      --connect-timeout 15 --max-time 45 --output "$response_file" \
      --write-out '%{http_code}' "$ambi_catalog_url") || return 1
  case "$status" in
    200) return 0 ;;
    401|403) printf 'API Key 无效或没有读取模型列表的权限。\n' >&2 ;;
    *) printf '模型列表请求失败，HTTP %s。\n' "$status" >&2 ;;
  esac
  return 1
}

ambi_choose_model() {
  /usr/bin/osascript -l JavaScript - "$1" <<'JAVASCRIPT'
ObjC.import('Foundation');
function run(argv) {
  var app = Application.currentApplication();
  app.includeStandardAdditions = true;
  var text = $.NSString.stringWithContentsOfFileEncodingError(argv[0], $.NSUTF8StringEncoding, null);
  var response = JSON.parse(ObjC.unwrap(text));
  if (!response || !Array.isArray(response.models)) throw new Error('该 API Key 未返回 Codex 模型目录，请使用支持 Codex 的令牌。');
  var models = response.models.map(function (item) { return item && item.slug; })
    .filter(function (id) { return typeof id === 'string' && id.length > 0 && !/[\x00-\x1f\x7f]/.test(id); })
    .filter(function (id, index, all) { return all.indexOf(id) === index; }).sort();
  if (!models.length) throw new Error('该 API Key 没有可用模型。');
  var preferred = models.filter(function (id) { return /^gpt-/.test(id) && !/image|audio|realtime/.test(id); })[0] || models[0];
  var chosen = app.chooseFromList(models, {withTitle: '选择 Sub2API 模型',
    withPrompt: '请选择支持 Responses 接口的对话模型。这里只读取模型列表，不会发起付费对话。',
    defaultItems: [preferred], okButtonName: '安装并配置', cancelButtonName: '取消'});
  if (!chosen) throw new Error('已取消配置。');
  return chosen[0];
}
JAVASCRIPT
}

ambi_find_app() {
  local candidate
  for candidate in /Applications/ChatGPT.app "$HOME/Applications/ChatGPT.app"; do
    if [ -d "$candidate" ]; then
      # ChatGPT Classic does not read Codex configuration.
      if [ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$candidate/Contents/Info.plist" 2>/dev/null || :)" = 'com.openai.codex' ]; then
        printf '%s\n' "$candidate"
        return 0
      fi
    fi
  done
  return 1
}

ambi_install_app() {
  local brew_path='' arch feed_url package_url app_path
  if ambi_find_app; then return 0; fi
  if command -v brew >/dev/null 2>&1; then
    brew_path=$(command -v brew)
  elif [ -x /opt/homebrew/bin/brew ]; then
    brew_path=/opt/homebrew/bin/brew
  elif [ -x /usr/local/bin/brew ]; then
    brew_path=/usr/local/bin/brew
  fi
  if [ -n "$brew_path" ]; then
    printf '正在通过 Homebrew 安装 ChatGPT…\n' >&2
    "$brew_path" install --cask chatgpt >&2 || return 1
    ambi_find_app
    return
  fi

  # No Homebrew or developer runtime is required for the official download.
  arch=$(uname -m)
  if [ "$(/usr/sbin/sysctl -n hw.optional.arm64 2>/dev/null || :)" = 1 ]; then arch=arm64; fi
  case "$arch" in
    arm64) feed_url='https://persistent.oaistatic.com/codex-app-prod/appcast.xml' ;;
    x86_64) feed_url='https://persistent.oaistatic.com/codex-app-prod/appcast-x64.xml' ;;
    *) printf '不支持的 Mac 处理器：%s\n' "$arch" >&2; return 1 ;;
  esac
  printf '正在从 OpenAI 官方更新源下载 ChatGPT…\n' >&2
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --silent --show-error \
    --connect-timeout 15 --max-time 60 "$feed_url" --output "$ambi_work_dir/appcast.xml" || return 1
  package_url=$(/usr/bin/xmllint --xpath 'string((//enclosure)[1]/@url)' "$ambi_work_dir/appcast.xml") || return 1
  case "$package_url" in
    https://persistent.oaistatic.com/codex-app-prod/ChatGPT-darwin-*.zip) ;;
    *) printf '官方更新源未返回可识别的安装包，请从 chatgpt.com/download 下载后重试。\n' >&2; return 1 ;;
  esac
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --show-error \
    --connect-timeout 15 --max-time 900 "$package_url" --output "$ambi_work_dir/ChatGPT.zip" >&2 || return 1
  /usr/bin/ditto -x -k "$ambi_work_dir/ChatGPT.zip" "$ambi_work_dir/unpacked" || return 1
  app_path="$ambi_work_dir/unpacked/ChatGPT.app"
  [ -d "$app_path" ] || return 1
  [ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$app_path/Contents/Info.plist")" = 'com.openai.codex' ] || return 1
  /usr/bin/codesign --verify --deep --strict "$app_path" >&2 || return 1
  /usr/sbin/spctl --assess --type execute "$app_path" >&2 || return 1
  mkdir -p "$HOME/Applications" || return 1
  if [ -e "$HOME/Applications/ChatGPT.app" ]; then
    printf '~/Applications/ChatGPT.app 已存在其他版本，请手动更新应用后重试。\n' >&2
    return 1
  fi
  /usr/bin/ditto "$app_path" "$HOME/Applications/ChatGPT.app" || return 1
  ambi_find_app
}

ambi_toml_escape() {
  local value=$1
  case "$value" in *[[:cntrl:]]*) return 1 ;; esac
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  printf '%s' "$value"
}

ambi_write_config() {
  local api_key=$1 model=$2 config_dir escaped_key escaped_model
  config_dir=${CODEX_HOME:-"$HOME/.codex"}
  mkdir -p "$config_dir"
  config_dir=$(cd "$config_dir" && pwd -P)
  ambi_config_file="$config_dir/config.toml"
  if [ -L "$ambi_config_file" ] || { [ -e "$ambi_config_file" ] && [ ! -f "$ambi_config_file" ]; }; then
    printf 'config.toml 是链接或非普通文件，请手动处理后重试。\n' >&2
    return 1
  fi
  escaped_key=$(ambi_toml_escape "$api_key")
  escaped_model=$(ambi_toml_escape "$model")
  ambi_config_temp=$(mktemp "$config_dir/.ambi-config.XXXXXX")
  chmod 600 "$ambi_config_temp"
  if [ -f "$ambi_config_file" ]; then
    ambi_backup_file=$(mktemp "$ambi_config_file.ambi-backup.XXXXXX")
    cat "$ambi_config_file" > "$ambi_backup_file"
    chmod 600 "$ambi_backup_file"
  fi
  cat > "$ambi_config_temp" <<TOML
# Generated by the Ambi Sub2API desktop setup.
model_provider = "ambi_vip"
model = "$escaped_model"

[model_providers.ambi_vip]
name = "Ambi Sub2API"
base_url = "$ambi_base_url"
model_catalog_url = "$ambi_catalog_url"
wire_api = "responses"
requires_openai_auth = false
experimental_bearer_token = "$escaped_key"
supports_websockets = false
TOML
  mv -f "$ambi_config_temp" "$ambi_config_file"
  ambi_config_temp=''
}

ambi_show_done() {
  local message="配置完成。\n\n中转地址：$ambi_base_url\n配置文件：$ambi_config_file"
  if [ -n "$ambi_backup_file" ]; then message="$message\n原配置备份：$ambi_backup_file"; fi
  message="$message\n\n请使用桌面应用中的 Codex 本地模式。若应用已打开，请先完全退出再重开以加载配置。ChatGPT 的其他功能仍使用各自的账号登录。"
  printf '%b\n' "$message"
  /usr/bin/osascript - "$(printf '%b' "$message")" <<'APPLESCRIPT'
on run argv
  display dialog (item 1 of argv) with title "Ambi Sub2API 配置成功" buttons {"好"} default button 1
end run
APPLESCRIPT
}

ambi_main() {
  local api_key model app_path
  [ "$(uname -s)" = Darwin ] || { printf '请在 macOS 运行此脚本。\n' >&2; return 1; }
  [ "$(/usr/bin/sw_vers -productVersion | cut -d. -f1)" -ge 13 ] || { printf 'ChatGPT 桌面版需要 macOS 13 或更新版本。\n' >&2; return 1; }
  umask 077
  trap ambi_finish EXIT
  ambi_work_dir=$(mktemp -d "${TMPDIR:-/tmp}/ambi-desktop.XXXXXX")
  ambi_error='已取消或未填写 API Key，原配置没有更改。'
  api_key=$(ambi_read_key)
  [ -n "$api_key" ]
  case "$api_key" in *[[:cntrl:]]*) return 1 ;; esac
  ambi_error='无法读取模型列表，请确认 sub.ambi.vip 的 API Key、额度与网络连接后重试。原配置没有更改。'
  ambi_fetch_models "$api_key" "$ambi_work_dir/models.json"
  ambi_error='模型选择未完成，原配置没有更改。'
  model=$(ambi_choose_model "$ambi_work_dir/models.json")
  ambi_error='应用安装未完成。请查看终端信息，或从 chatgpt.com/download 安装新版桌面应用后重新运行脚本。原配置没有更改。'
  app_path=$(ambi_install_app)
  ambi_error='配置写入未完成，请检查 Codex 配置目录的权限和磁盘空间。原配置备份位于 config.toml 同目录。'
  ambi_write_config "$api_key" "$model"
  unset api_key
  # Launch failure does not invalidate a successfully written configuration.
  /usr/bin/open -a "$app_path" || :
  ambi_show_done
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then ambi_main "$@"; fi
