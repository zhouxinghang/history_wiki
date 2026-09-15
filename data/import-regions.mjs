#!/usr/bin/env node
// 依据 data/level1-regions.json 通过管理接口批量创建地区。
//
// 用法:
//   HW_BASE=http://homelab.com:9003 HW_EMAIL=you@example.com HW_PASSWORD=... \
//     node data/import-regions.mjs
// 或直接复用浏览器会话:
//   HW_BASE=http://homelab.com:9003 HW_SESSION_COOKIE=<session值> HW_CSRF_TOKEN=<csrf值> \
//     node data/import-regions.mjs
//
// 说明:
// - 使用 Idempotency-Key = "level1-regions/<key>"，同一账号重复运行不会重复创建，
//   命中幂等记录时接口返回既有地区（响应头 Idempotency-Replayed: true）。
// - 输出 data/level1-region-ids.json：key/name -> 地区 UUID，供事件录入使用。
import { readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const base = (process.env.HW_BASE ?? 'http://homelab.com:9003').replace(/\/$/, '')
const email = process.env.HW_EMAIL
const password = process.env.HW_PASSWORD
const sessionValue = process.env.HW_SESSION_COOKIE
const csrfValue = process.env.HW_CSRF_TOKEN
const sessionCookieName = process.env.HW_SESSION_COOKIE_NAME ?? 'history_wiki_session'
const csrfCookieName = process.env.HW_CSRF_COOKIE_NAME ?? 'history_wiki_csrf'

if (!(sessionValue && csrfValue) && !(email && password)) {
  console.error('必须设置 HW_EMAIL/HW_PASSWORD，或 HW_SESSION_COOKIE/HW_CSRF_TOKEN')
  process.exit(2)
}

const source = JSON.parse(await readFile(join(here, 'level1-regions.json'), 'utf8'))

function fail(message, body) {
  console.error(message)
  if (body !== undefined) console.error(typeof body === 'string' ? body : JSON.stringify(body, null, 2))
  process.exit(1)
}

async function sessionFromEnv() {
  const response = await fetch(`${base}/api/v1/admin`, {
    headers: { Cookie: `${sessionCookieName}=${sessionValue}` },
  })
  const text = await response.text()
  if (!response.ok) fail(`会话无效 (${response.status})`, text)
  const user = JSON.parse(text).user
  console.log(`已登录: ${user.email ?? user.id} (${user.role})`)
  return { cookieHeader: `${sessionCookieName}=${sessionValue}; ${csrfCookieName}=${csrfValue}`, csrfToken: csrfValue }
}

async function login() {
  const response = await fetch(`${base}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Origin: base },
    body: JSON.stringify({ email, password }),
  })
  const text = await response.text()
  if (!response.ok) fail(`登录失败 (${response.status})`, text)

  const cookies = response.headers.getSetCookie().map((raw) => raw.split(';')[0])
  const csrfCookie = cookies.find((cookie) => cookie.toLowerCase().includes('csrf'))
  if (!csrfCookie) fail('登录响应未返回 CSRF Cookie', cookies)
  const csrfToken = csrfCookie.slice(csrfCookie.indexOf('=') + 1)

  const user = JSON.parse(text).user
  console.log(`已登录: ${user.email ?? user.id} (${user.role})`)
  return { cookieHeader: cookies.join('; '), csrfToken }
}

async function createRegion(session, region) {
  const response = await fetch(`${base}/api/v1/admin/regions`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Origin: base,
      Cookie: session.cookieHeader,
      'X-CSRF-Token': session.csrfToken,
      'Idempotency-Key': `level1-regions/${region.key}`,
    },
    body: JSON.stringify({ name: region.name, disambiguationLabel: region.disambiguationLabel ?? null }),
  })
  const text = await response.text()
  if (!response.ok) fail(`创建「${region.name}」失败 (${response.status})`, text)
  return { region: JSON.parse(text), replayed: response.headers.get('Idempotency-Replayed') === 'true' }
}

const session = sessionValue && csrfValue ? await sessionFromEnv() : await login()

const results = []
for (const region of source.regions) {
  const { region: created, replayed } = await createRegion(session, region)
  results.push({ key: region.key, name: created.name, id: created.id, replayed })
  console.log(`${String(results.length).padStart(2)}/${source.regions.length}  ${created.name}  ${created.id}${replayed ? '  (已存在)' : ''}`)
}

const byName = Object.fromEntries(results.map((r) => [r.name, r.id]))
const byKey = Object.fromEntries(results.map((r) => [r.key, r.id]))
await writeFile(
  join(here, 'level1-region-ids.json'),
  `${JSON.stringify({ base, generatedAt: new Date().toISOString(), regions: results, byName, byKey }, null, 2)}\n`,
  'utf8',
)
console.log(`\n已写入 data/level1-region-ids.json（${results.length} 个地区，新增 ${results.filter((r) => !r.replayed).length} 个）`)
