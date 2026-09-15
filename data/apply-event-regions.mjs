#!/usr/bin/env node
// 为已存在的 level1 事件草稿补齐 regionIds，其余字段原样保留。
//
// 用法:
//   HW_BASE=http://homelab.com:9003 \
//   HW_SESSION_COOKIE=<session值> HW_CSRF_TOKEN=<csrf值> \
//     node data/apply-event-regions.mjs
//
// 行为:
// - 只处理 data/level1-regions.json 中登记的事件 slug；
// - 先读取草稿现状，仅在 regionIds 与目标不一致时才 PATCH，避免无谓版本递增；
// - placeIds/periodIds/figureIds/topicTagIds 及全部正文字段按现状回传，不覆盖人工修改；
// - 使用 If-Match: "draft-<lockVersion>" 做并发保护。
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
const { byName } = JSON.parse(await readFile(join(here, 'level1-region-ids.json'), 'utf8'))
const targetNames = Object.fromEntries(eventRegions.map((e) => [e.slug, e.regions]))

function nameToID(name) {
  const id = byName[name]
  if (!id) throw new Error(`地区 UUID 未知: ${name}`)
  return id
}

async function api(path, init = {}) {
  for (let attempt = 0; attempt < 10; attempt += 1) {
    const response = await fetch(`${base}${path}`, { ...init, headers: { ...headers, ...(init.headers ?? {}) } })
    const text = await response.text()
    if (response.status === 429 && attempt < 9) {
      const waitSeconds = Number(response.headers.get('Retry-After') ?? '2')
      console.log(`  · 触发管理写入限流，等待 ${waitSeconds}s 后重试`)
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
const events = JSON.parse(list.text).events
console.log(`事件总数 ${events.length}，目标 ${Object.keys(targetNames).length}`)

const results = []
for (const event of events) {
  const names = targetNames[event.slug]
  if (!names) continue

  const target = [...new Set(names.map(nameToID))].sort()

  const detail = await api(`/api/v1/admin/events/${event.id}`)
  if (!detail.response.ok) {
    results.push({ slug: event.slug, status: 'read_failed', detail: detail.text })
    console.error(`✗ ${event.slug} 读取失败 (${detail.response.status})`)
    continue
  }
  const managed = JSON.parse(detail.text)
  const draft = managed.draft
  if (!draft) {
    results.push({ slug: event.slug, status: 'no_draft' })
    console.error(`✗ ${event.slug} 没有活动草稿`)
    continue
  }

  const current = draft.regions.map((r) => r.id).sort()
  if (JSON.stringify(current) === JSON.stringify(target)) {
    results.push({ slug: event.slug, status: 'already_set', regions: target })
    console.log(`= ${event.slug} 已是目标地区，跳过`)
    continue
  }

  const body = {
    title: draft.title,
    summary: draft.summary,
    narrative: draft.narrative,
    time: draft.time,
    primaryCategory: draft.primaryCategory,
    prominence: draft.prominence,
    displayOrder: draft.displayOrder,
    regionIds: target,
    placeIds: draft.places.map((p) => p.id),
    periodIds: draft.periods.map((p) => p.id),
    figureIds: draft.figures.map((f) => f.id),
    topicTagIds: draft.topicTags.map((t) => t.id),
  }

  const patched = await api(`/api/v1/admin/events/${event.id}/draft`, {
    method: 'PATCH',
    headers: { 'If-Match': `"draft-${draft.lockVersion}"` },
    body: JSON.stringify(body),
  })
  if (!patched.response.ok) {
    results.push({ slug: event.slug, status: 'patch_failed', detail: patched.text })
    console.error(`✗ ${event.slug} 更新失败 (${patched.response.status}): ${patched.text}`)
    continue
  }
  results.push({ slug: event.slug, status: 'updated', regions: target })
  console.log(`✓ ${event.slug} 写入 ${names.length} 个地区: ${names.join('、')}`)
}

const summary = results.reduce((acc, r) => ({ ...acc, [r.status]: (acc[r.status] ?? 0) + 1 }), {})
await writeFile(
  join(here, 'event-regions-applied.json'),
  `${JSON.stringify({ base, generatedAt: new Date().toISOString(), summary, results }, null, 2)}\n`,
  'utf8',
)
console.log('\n汇总:', JSON.stringify(summary))
console.log('明细: data/event-regions-applied.json')
