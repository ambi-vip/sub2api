import { describe, expect, it } from 'vitest'
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  buildCodexQuickConfigToml,
  buildMacLinuxCodexQuickConfigScript,
  buildWindowsCodexQuickConfigScript,
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
  })

  it('generates rerunnable scripts for macOS/Linux and Windows', () => {
    const input = {
      apiKey: 'sk-special-`$"\\-测试',
      baseUrl: 'https://example.com/v1',
      platform: 'openai' as const
    }
    const unixScript = buildMacLinuxCodexQuickConfigScript(input)
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
    expect(new TextDecoder().decode(new Uint8Array(decodedConfig))).toContain('experimental_bearer_token')
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
})
