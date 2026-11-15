'use strict';

const toast = document.getElementById('toast');
let toastTimer;
function notify(message) {
  toast.textContent = message;
  toast.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toast.classList.remove('show'), 3500);
}

async function copyText(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
    } else {
      const area = document.createElement('textarea');
      area.value = text;
      area.style.position = 'fixed';
      area.style.opacity = '0';
      document.body.append(area);
      try {
        area.select();
        if (!document.execCommand('copy')) throw new Error('copy unavailable');
      } finally { area.remove(); }
    }
    notify('已复制到剪贴板');
  } catch {
    notify('复制失败，请选中命令后手动复制。');
  }
}

document.querySelectorAll('[data-copy]').forEach(button => {
  button.addEventListener('click', () => copyText(button.dataset.copy));
});

const installerPayloads = JSON.parse(document.getElementById('setup-installers').textContent);
document.querySelectorAll('[data-installer]').forEach(button => {
  button.addEventListener('click', () => {
    try {
      const payload = installerPayloads[button.dataset.installer];
      const bytes = Uint8Array.from(atob(payload.base64), character => character.charCodeAt(0));
      const blob = new Blob([bytes], { type: payload.type });
      const downloadUrl = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = downloadUrl;
      link.download = payload.filename;
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(downloadUrl), 10000);
      notify('已发起下载。请打开下载文件，按提示输入 API Key。');
    } catch {
      notify('下载未完成，请重新打开页面后重试。');
    }
  });
});

const baseUrl = document.getElementById('baseUrl');
const apiKey = document.getElementById('apiKey');
const preview = document.getElementById('preview');
function updatePreview() {
  preview.textContent = JSON.stringify({ base_url: baseUrl.value, api_key: apiKey.value ? '••••••••（已隐藏）' : '你的 Sub2API API Key' }, null, 2);
}
apiKey.addEventListener('input', updatePreview);
document.getElementById('copyConfig').addEventListener('click', () => {
  if (!apiKey.value.trim()) {
    notify('请先填写 API Key；下载脚本时可直接在本机弹窗中填写。');
    apiKey.focus();
    return;
  }
  copyText(JSON.stringify({ base_url: baseUrl.value, api_key: apiKey.value.trim() }, null, 2));
});
updatePreview();
