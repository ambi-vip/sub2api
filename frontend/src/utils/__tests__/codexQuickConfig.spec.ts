import { describe, expect, it } from 'vitest'
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  buildCodexQuickConfigToml,
  buildMacLinuxCodexQuickConfigScript,
  buildWindowsCmdCodexQuickConfigScript,
  buildWindowsCodexQuickConfigScript,
  encodeUtf8Base64,
  normalizeCodexBaseUrl
} from '@/utils/codexQuickConfig'

describe('codexQuickConfig', () => {
  it('normalizes the Codex API base URL without duplicating /v1', () => {
    expect(normalizeCodexBaseUrl('https://example.com')).toBe('https://example.com/v1')
    expect(normalizeCodexBaseUrl('https://example.com/v1/')).toBe('https://example.com/v1')
  })

  it('writes API Key Mode credentials into the Codex provider config', () => {
    const config = buildCodexQuickConfigToml({
      apiKey: 'sk-quick-test',
      baseUrl: 'https://example.com',
      platform: 'openai'
    })

    expect(config).toContain('requires_openai_auth = false')
    expect(config).toContain('experimental_bearer_token = "sk-quick-test"')
    expect(config).toContain('http_headers = { "x-openai-actor-authorization" = "local-image-extension" }')
    expect(config).toContain('base_url = "https://example.com/v1"')
    expect(config).toContain('model_provider = "OpenAI"')
    expect(config).not.toContain('model_catalog_json')
    expect(config).not.toContain('codex-models.json')
  })

  it('adds the model catalog setting only when a catalog is supplied', () => {
    const config = buildCodexQuickConfigToml({
      apiKey: 'sk-quick-test',
      baseUrl: 'https://example.com',
      platform: 'openai',
      modelCatalogContent: '{"models":[{"slug":"gpt-test"}]}',
      modelCatalogPath: '%USERPROFILE%\\.codex\\codex-models.json'
    })

    expect(config).toContain('model_catalog_json = "%USERPROFILE%\\\\.codex\\\\codex-models.json"')
  })

  it('generates rerunnable scripts for macOS/Linux and Windows', () => {
    const input = {
      apiKey: 'sk-special-`$"\\-测试',
      baseUrl: 'https://example.com/v1',
      platform: 'openai' as const
    }
    const unixScript = buildMacLinuxCodexQuickConfigScript(input)
    const cmdScript = buildWindowsCmdCodexQuickConfigScript(input)
    const windowsScript = buildWindowsCodexQuickConfigScript(input)

    expect(unixScript).toContain('#!/usr/bin/env bash')
    expect(unixScript).toContain('mkdir -p "${CONFIG_DIR}"')
    expect(unixScript).toContain('mktemp "${CONFIG_FILE}.tmp.XXXXXX"')
    expect(unixScript).toContain('mv -f "${TMP_FILE}" "${CONFIG_FILE}"')
    expect(unixScript).toContain('base64 --decode')
    expect(unixScript).toContain('base64 -D')

    expect(windowsScript).toContain('$ErrorActionPreference = \'Stop\'')
    expect(windowsScript).toContain('New-Item -ItemType Directory')
    expect(windowsScript).toContain('Move-Item -LiteralPath $tempFile -Destination $configFile -Force')
    expect(windowsScript).toContain('[Convert]::FromBase64String($payload)')

    const payload = windowsScript.match(/\$payload = '([^']+)'/)?.[1]
    expect(payload).toBeDefined()
    const decodedConfig = atob(payload!)
      .split('')
      .map((character) => character.charCodeAt(0))
    const decodedWindowsConfig = new TextDecoder().decode(new Uint8Array(decodedConfig))
    expect(decodedWindowsConfig).toContain('experimental_bearer_token')
    expect(decodedWindowsConfig).not.toContain('model_catalog_json')
    expect(decodedWindowsConfig).not.toContain('codex-models.json')

    expect(cmdScript).toContain('@echo off')
    expect(cmdScript).toContain('set "CONFIG_DIR=%USERPROFILE%\\.codex"')
    expect(cmdScript).toContain('powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass')
    expect(cmdScript).toContain('move /Y "%TEMP_FILE%" "%CONFIG_FILE%"')
    expect(cmdScript).toContain('pause')
    const cmdPayload = cmdScript.match(/set "SUB2API_CODEX_PAYLOAD=([^\"]+)"/)?.[1]
    expect(cmdPayload).toBeDefined()
    const decodedCmdConfig = atob(cmdPayload!)
      .split('')
      .map((character) => character.charCodeAt(0))
    const decodedCmdConfigText = new TextDecoder().decode(new Uint8Array(decodedCmdConfig))
    expect(decodedCmdConfigText).toContain('experimental_bearer_token')
    expect(decodedCmdConfigText).not.toContain('model_catalog_json')
    expect(decodedCmdConfigText).not.toContain('codex-models.json')
  })

  it('embeds and overwrites codex-models.json on all supported script formats', () => {
    const catalog = JSON.stringify({
      object: 'codex.models',
      models: [{ slug: 'gpt-test', display_name: 'Test model' }]
    }, null, 2)
    const input = {
      apiKey: 'sk-catalog-test',
      baseUrl: 'https://example.com/v1',
      platform: 'openai' as const,
      modelCatalogContent: catalog,
      modelCatalogPath: '~/.codex/codex-models.json'
    }

    const unixScript = buildMacLinuxCodexQuickConfigScript(input)
    const unixPayload = unixScript.match(/MODEL_CATALOG_PAYLOAD='([^']+)'/)?.[1]
    expect(unixPayload).toBeDefined()
    expect(decodeBase64Utf8(unixPayload!)).toBe(catalog)
    expect(unixScript).toContain('mv -f "${CATALOG_TMP_FILE}" "${CONFIG_DIR}/codex-models.json"')

    const powershellScript = buildWindowsCodexQuickConfigScript(input)
    const powershellPayload = powershellScript.match(/\$catalogPayload = '([^']+)'/)?.[1]
    expect(powershellPayload).toBeDefined()
    expect(decodeBase64Utf8(powershellPayload!)).toBe(catalog)
    expect(powershellScript).toContain("Move-Item -LiteralPath $catalogTempFile -Destination (Join-Path $configDir 'codex-models.json') -Force")

    const cmdScript = buildWindowsCmdCodexQuickConfigScript(input)
    const cmdChunks = [...cmdScript.matchAll(/(?:^|\n)(?:> |>> )"%SUB2API_CODEX_CATALOG_TEMP%" echo ([A-Za-z0-9+/=]+)/g)]
      .map((match) => match[1])
    expect(cmdChunks.join('')).toBe(encodeUtf8Base64(catalog))
    expect(cmdScript).toContain('ReadAllText($env:SUB2API_CODEX_CATALOG_TEMP)')
    expect(cmdScript).toContain('move /Y "%SUB2API_CODEX_CATALOG_TEMP%" "%CONFIG_DIR%\\codex-models.json"')
    expect(cmdScript).not.toContain('SUB2API_CODEX_CATALOG_PAYLOAD=')
    expect(cmdScript.indexOf('mkdir "%CONFIG_DIR%"')).toBeLessThan(
      cmdScript.indexOf('> "%SUB2API_CODEX_CATALOG_TEMP%" echo')
    )
    expect(cmdScript.indexOf('move /Y "%SUB2API_CODEX_CATALOG_TEMP%"')).toBeLessThan(
      cmdScript.indexOf('move /Y "%TEMP_FILE%" "%CONFIG_FILE%"')
    )
  })

  it('does not write a model catalog when catalog import is disabled', () => {
    const inputs = {
      apiKey: 'sk-no-catalog',
      baseUrl: 'https://example.com/v1',
      platform: 'openai' as const
    }

    for (const script of [
      buildMacLinuxCodexQuickConfigScript(inputs),
      buildWindowsCodexQuickConfigScript(inputs),
      buildWindowsCmdCodexQuickConfigScript(inputs)
    ]) {
      expect(script).not.toContain('codex-models.json')
      expect(script).not.toContain('MODEL_CATALOG_PAYLOAD')
      expect(script).not.toContain('$catalogPayload')
      expect(script).not.toContain('SUB2API_CODEX_CATALOG_TEMP=%CONFIG_FILE%')
    }
  })

  it('runs the macOS/Linux script and overwrites the existing Codex config', () => {
    const home = mkdtempSync(join(tmpdir(), 'sub2api-codex-'))
    const scriptPath = join(home, 'setup.sh')
    try {
      writeFileSync(scriptPath, buildMacLinuxCodexQuickConfigScript({
        apiKey: 'sk-runtime-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai'
      }))
      chmodSync(scriptPath, 0o700)

      execFileSync('bash', [scriptPath], { env: { ...process.env, HOME: home } })
      const configPath = join(home, '.codex', 'config.toml')
      const firstRun = readFileSync(configPath, 'utf8')
      expect(firstRun).toContain('experimental_bearer_token = "sk-runtime-test"')

      execFileSync('bash', [scriptPath], { env: { ...process.env, HOME: home } })
      expect(readFileSync(configPath, 'utf8')).toBe(firstRun)
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })

  it('runs the macOS/Linux script and overwrites the model catalog', () => {
    const home = mkdtempSync(join(tmpdir(), 'sub2api-codex-catalog-'))
    const scriptPath = join(home, 'setup.sh')
    const catalogPath = join(home, '.codex', 'codex-models.json')
    const firstCatalog = '{"models":[{"slug":"gpt-first"}]}'
    const secondCatalog = '{"models":[{"slug":"gpt-second"}]}'
    try {
      writeFileSync(scriptPath, buildMacLinuxCodexQuickConfigScript({
        apiKey: 'sk-runtime-catalog-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai',
        modelCatalogContent: firstCatalog
      }))
      chmodSync(scriptPath, 0o700)
      execFileSync('bash', [scriptPath], { env: { ...process.env, HOME: home } })

      const configPath = join(home, '.codex', 'config.toml')
      expect(readFileSync(configPath, 'utf8')).toContain('model_catalog_json = "~/.codex/codex-models.json"')
      expect(readFileSync(catalogPath, 'utf8')).toBe(firstCatalog)

      writeFileSync(scriptPath, buildMacLinuxCodexQuickConfigScript({
        apiKey: 'sk-runtime-catalog-test',
        baseUrl: 'https://example.com/v1',
        platform: 'openai',
        modelCatalogContent: secondCatalog
      }))
      execFileSync('bash', [scriptPath], { env: { ...process.env, HOME: home } })
      expect(readFileSync(catalogPath, 'utf8')).toBe(secondCatalog)
    } finally {
      rmSync(home, { recursive: true, force: true })
    }
  })
})

function decodeBase64Utf8(value: string): string {
  const bytes = atob(value)
    .split('')
    .map((character) => character.charCodeAt(0))
  return new TextDecoder().decode(new Uint8Array(bytes))
}
