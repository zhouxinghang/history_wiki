#!/usr/bin/env node
// 发布 level1 事件：对每条存在活动草稿且尚未发布的事件调用 publish。
//
// 用法:
//   HW_BASE=http://homelab.com:9003 \
//   HW_SESSION_COOKIE=<session值> HW_CSRF_TOKEN=<csrf值> \
//     node data/publish-events.mjs
//
// 行为:
// - 只处理 data/level1-regions.json 中登记的事件 slug；
// - 已发布 / 无草稿的事件跳过，可安全重复运行；
// - 使用 If-Match: "draft-<lockVersion>" 并发保护，并在触发限流时按 Retry-After 重试。
import { readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const base = (process.env.HW_BASE ?? 'http://homelab.com:9003').replace(/\/$/, '')
const sessionValue = process.env.HW_SESSION_COOKIE
const csrfValue = process.env.HW_CSRF_TOKEN
const sessionCookieName = process.env.HW_SESSION_COOKIE_NAME ?? 'history_wiki_session'
const csrfCookieName = process.env.HW_CSRF_COOKIE_NAME ?? 'history_wiki_csrf'

if (!sessionValue || !csrfValue) {
  console.error('必须设置 HW_SESSION_COOKIE 与 HW_CSRF_TOKEN')
  process.exit(2)
}

const headers = {
  'Content-Type': 'application/json',
  Origin: base,
  Cookie: `${sessionCookieName}=${sessionValue}; ${csrfCookieName}=${csrfValue}`,
  'X-CSRF-Token': csrfValue,
}

const { eventRegions } = JSON.parse(await readFile(join(here, 'level1-regions.json'), 'utf8'))
const targetSlugs = new Set(eventRegions.map((e) => e.slug))

async function api(path, init = {}) {
  for (let attempt = 0; attempt < 15; attempt += 1) {
    const response = await fetch(`${base}${path}`, { ...init, headers: { ...headers, ...(init.headers ?? {}) } })
    const text = await response.text()
    if (response.status === 429 && attempt < 14) {
      const waitSeconds = Number(response.headers.get('Retry-After') ?? '2')
      console.log(`  · 触发限流，等待 ${waitSeconds}s 后重试`)
      await new Promise((resolve) => setTimeout(resolve, (waitSeconds + 0.5) * 1000))
      continue
    }
    return { response, text }
  }
  throw new Error(`retry exhausted: ${path}`)
}

const list = await api('/api/v1/admin/events')
if (!list.response.ok) {
  console.error(`读取事件列表失败 (${list.response.status}):`, list.text)
  process.exit(1)
}
const events = JSON.parse(list.text).events.filter((event) => targetSlugs.has(event.slug))
console.log(`目标事件 ${events.length}`)

const results = []
for (const event of events) {
  if (event.publicationStatus === 'published') {
    results.push({ slug: event.slug, status: 'already_published' })
    console.log(`= ${event.slug} 已发布，跳过`)
    continue
  }
  if (!event.draft) {
    results.push({ slug: event.slug, status: 'no_draft' })
    console.error(`✗ ${event.slug} 没有活动草稿（状态 ${event.publicationStatus}）`)
    continue
  }

  const published = await api(`/api/v1/admin/events/${event.id}/publish`, {
    method: 'POST',
    headers: { 'If-Match': `"draft-${event.draft.lockVersion}"` },
  })
  if (!published.response.ok) {
    results.push({ slug: event.slug, status: 'publish_failed', detail: published.text })
    console.error(`✗ ${event.slug} 发布失败 (${published.response.status}): ${published.text}`)
    continue
  }
  const revisionID = (published.response.headers.get('Location') ?? '').split('/').pop() || undefined
  results.push({ slug: event.slug, status: 'published', revisionId: revisionID })
  console.log(`✓ ${event.slug} 已发布`)
}

const summary = results.reduce((acc, r) => ({ ...acc, [r.status]: (acc[r.status] ?? 0) + 1 }), {})
await writeFile(
  join(here, 'events-publish-result.json'),
  `${JSON.stringify({ base, generatedAt: new Date().toISOString(), summary, results }, null, 2)}\n`,
  'utf8',
)
console.log('\n汇总:', JSON.stringify(summary))
console.log('明细: data/events-publish-result.json')
