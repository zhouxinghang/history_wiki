import { spawn } from 'node:child_process'
import { access, mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

async function main() {
  const root = process.cwd()
  const previewPort = await availablePort()
  const debuggingPort = await availablePort()
  const previewUrl = `http://127.0.0.1:${previewPort}/?demo=performance`
  const overlapUrl = `${previewUrl}&from=99&to=101`
  const outputPath = path.join(root, 'docs/performance/issue-11-results.json')
  const chromeUserData = await mkdtemp(path.join(os.tmpdir(), 'history-wiki-chrome-'))
  let preview
  let chrome
  let session

  try {
    preview = spawn(
      path.join(root, 'node_modules/.bin/vite'),
      ['preview', '--host', '127.0.0.1', '--port', String(previewPort), '--strictPort'],
      { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] },
    )
    await waitForHttp(`http://127.0.0.1:${previewPort}/`)

    const chromePath = await findChrome()
    chrome = spawn(
      chromePath,
      [
        '--headless=new',
        '--disable-gpu',
        '--disable-background-timer-throttling',
        '--disable-backgrounding-occluded-windows',
        '--disable-renderer-backgrounding',
        '--no-first-run',
        '--no-default-browser-check',
        `--remote-debugging-port=${debuggingPort}`,
        `--user-data-dir=${chromeUserData}`,
        'about:blank',
      ],
      { stdio: ['ignore', 'ignore', 'pipe'] },
    )
    await waitForHttp(`http://127.0.0.1:${debuggingPort}/json/version`)

    const target = await fetchJson(
      `http://127.0.0.1:${debuggingPort}/json/new?${encodeURIComponent('about:blank')}`,
      { method: 'PUT' },
    )
    session = new CdpSession(target.webSocketDebuggerUrl)
    await session.open()
    await Promise.all([
      session.send('Page.enable'),
      session.send('Runtime.enable'),
      session.send('Performance.enable'),
    ])
    await session.send('Page.addScriptToEvaluateOnNewDocument', {
      source: performanceCollectorSource,
    })
    await session.send('Emulation.setDeviceMetricsOverride', {
      width: 1440,
      height: 1000,
      deviceScaleFactor: 1,
      mobile: false,
    })
    await session.send('Page.navigate', { url: previewUrl })
    await waitForApplication(session)

    const browser = await session.send('Browser.getVersion')
    const desktop = await smokeCheck(session, 'desktop')
    await session.send('Page.navigate', { url: overlapUrl })
    await waitForApplication(session)
    const domAccessibility = await measureDomAccessibility(session)
    await session.send('Page.navigate', { url: previewUrl })
    await waitForApplication(session)
    const drag = await measureDrag(session)
    await session.send('Page.navigate', { url: previewUrl })
    await waitForApplication(session)
    const zoom = await measureZoom(session)
    await session.send('Page.navigate', { url: previewUrl })
    await waitForApplication(session)
    const filter = await measureFilter(session)

    await session.send('Emulation.setDeviceMetricsOverride', {
      width: 390,
      height: 844,
      deviceScaleFactor: 1,
      mobile: true,
    })
    await session.send('Page.navigate', { url: previewUrl })
    await waitForApplication(session)
    const mobile = await smokeCheck(session, 'mobile')

    const result = {
      generatedAt: new Date().toISOString(),
      sourceEvents: desktop.sourceTotal,
      browser: browser.product,
      viewportChecks: { desktop, mobile },
      domAccessibility,
      scenarios: { filter, drag, zoom },
      thresholds: {
        filterLatencyMs: 100,
        minimumInteractionFps: 50,
        sustainedLongTaskMs: 100,
        maximumEventDomNodes: 300,
      },
    }
    result.passed = evaluateResult(result)

    await mkdir(path.dirname(outputPath), { recursive: true })
    await writeFile(outputPath, `${JSON.stringify(result, null, 2)}\n`)
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
    if (!result.passed) process.exitCode = 1
  } finally {
    session?.close()
    await stopProcess(chrome)
    await stopProcess(preview)
    await rm(chromeUserData, {
      recursive: true,
      force: true,
      maxRetries: 5,
      retryDelay: 100,
    }).catch(() => {})
  }
}

async function measureFilter(cdp) {
  return evaluate(cdp, `
    (async () => {
      window.__historyWikiPerformance.reset()
      const root = document.querySelector('.app-shell')
      const beforeRevision = Number(root.dataset.queryRevision)
      const checkbox = [...document.querySelectorAll('label')]
        .find((label) => label.textContent.trim() === '政治')
        ?.querySelector('input')
      if (!checkbox) throw new Error('未找到政治主分类筛选器')
      const startedAt = performance.now()
      checkbox.click()
      while (Number(root.dataset.queryRevision) <= beforeRevision) {
        await new Promise(requestAnimationFrame)
      }
      await new Promise(requestAnimationFrame)
      const latencyMs = performance.now() - startedAt
      return { latencyMs, ...window.__historyWikiPerformance.summary(startedAt) }
    })()
  `)
}

async function measureDrag(cdp) {
  const bounds = await evaluate(cdp, `
    (() => {
      window.__historyWikiPerformance.reset()
      const rect = document.querySelector('.timeline__viewport').getBoundingClientRect()
      return { left: rect.left, top: rect.top, width: rect.width, height: rect.height, startedAt: performance.now() }
    })()
  `)
  const y = bounds.top + Math.min(bounds.height / 2, 260)
  const startX = bounds.left + bounds.width * 0.72
  const endX = bounds.left + bounds.width * 0.28
  const steps = 96

  await cdp.send('Input.dispatchMouseEvent', {
    type: 'mousePressed',
    x: startX,
    y,
    button: 'left',
    buttons: 1,
    clickCount: 1,
  })
  for (let index = 1; index <= steps; index += 1) {
    const x = startX + ((endX - startX) * index) / steps
    await cdp.send('Input.dispatchMouseEvent', {
      type: 'mouseMoved',
      x,
      y,
      button: 'left',
      buttons: 1,
    })
    await delay(16)
  }
  await cdp.send('Input.dispatchMouseEvent', {
    type: 'mouseReleased',
    x: endX,
    y,
    button: 'left',
    buttons: 0,
    clickCount: 1,
  })
  await delay(150)
  return evaluate(
    cdp,
    `window.__historyWikiPerformance.summary(${bounds.startedAt})`,
  )
}

async function measureZoom(cdp) {
  const bounds = await evaluate(cdp, `
    (() => {
      window.__historyWikiPerformance.reset()
      const rect = document.querySelector('.timeline__viewport').getBoundingClientRect()
      return { x: rect.left + rect.width / 2, y: rect.top + Math.min(rect.height / 2, 260), startedAt: performance.now() }
    })()
  `)

  for (let index = 0; index < 90; index += 1) {
    await cdp.send('Input.dispatchMouseEvent', {
      type: 'mouseWheel',
      x: bounds.x,
      y: bounds.y,
      deltaX: 0,
      deltaY: index % 2 === 0 ? -40 : 40,
    })
    await delay(16)
  }
  await delay(150)
  return evaluate(
    cdp,
    `window.__historyWikiPerformance.summary(${bounds.startedAt})`,
  )
}

async function smokeCheck(cdp, viewport) {
  return evaluate(cdp, `
    (() => ({
      viewport: ${JSON.stringify(viewport)},
      sourceTotal: Number(document.querySelector('.app-shell').dataset.sourceTotal),
      timelineVisible: Boolean(document.querySelector('.timeline__viewport')?.getClientRects().length),
      mobileFilterVisible: Boolean(document.querySelector('.mobile-filter-trigger')?.getClientRects().length),
      horizontalOverflowPx: Math.max(0, document.documentElement.scrollWidth - window.innerWidth),
      eventDomNodes: window.__historyWikiPerformance.eventNodeCount(),
    }))()
  `)
}

function evaluateResult(result) {
  const scenarios = Object.values(result.scenarios)
  return (
    result.sourceEvents === 10_000 &&
    result.scenarios.filter.latencyMs < result.thresholds.filterLatencyMs &&
    result.scenarios.drag.fps >= result.thresholds.minimumInteractionFps &&
    result.scenarios.zoom.fps >= result.thresholds.minimumInteractionFps &&
    scenarios.every(
      (scenario) =>
        scenario.sustainedLongTaskCount === 0 &&
        scenario.maxEventDomNodes <= result.thresholds.maximumEventDomNodes,
    ) &&
    result.domAccessibility.previewEventNodes === 12 &&
    result.domAccessibility.expandedPageEventNodes <=
      result.thresholds.maximumEventDomNodes &&
    result.domAccessibility.nextPageAvailable &&
    result.viewportChecks.desktop.timelineVisible &&
    !result.viewportChecks.desktop.mobileFilterVisible &&
    result.viewportChecks.mobile.timelineVisible &&
    result.viewportChecks.mobile.mobileFilterVisible &&
    result.viewportChecks.desktop.horizontalOverflowPx === 0 &&
    result.viewportChecks.mobile.horizontalOverflowPx === 0
  )
}

async function measureDomAccessibility(cdp) {
  return evaluate(cdp, `
    (async () => {
      const cluster = document.querySelector('.timeline-cluster')
      if (!cluster) throw new Error('密集时间范围未生成聚合簇')
      cluster.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))
      await new Promise(requestAnimationFrame)
      const previewEventNodes = document.querySelectorAll('.timeline-cluster-preview li').length
      cluster.click()
      await new Promise(requestAnimationFrame)
      const expandedPageEventNodes = document.querySelectorAll('.timeline__overlap-list li').length
      const nextPage = [...document.querySelectorAll('.timeline__overlap-pagination button')]
        .find((button) => button.textContent.trim() === '下一页')
      return {
        clusterEventCount: Number(cluster.querySelector('strong')?.textContent ?? 0),
        previewEventNodes,
        expandedPageEventNodes,
        nextPageAvailable: Boolean(nextPage && !nextPage.disabled),
        totalEventDomNodes: window.__historyWikiPerformance.eventNodeCount(),
      }
    })()
  `)
}

async function waitForApplication(cdp) {
  await evaluate(cdp, `
    (async () => {
      const deadline = performance.now() + 15000
      while (performance.now() < deadline) {
        const root = document.querySelector('.app-shell')
        if (
          root?.dataset.queryStatus === 'ready' &&
          Number(root.dataset.sourceTotal) === 10000 &&
          document.querySelector('.timeline__viewport')
        ) return true
        await new Promise((resolve) => setTimeout(resolve, 50))
      }
      throw new Error('性能演示页面未在 15 秒内准备完成')
    })()
  `)
}

async function evaluate(cdp, expression) {
  const response = await cdp.send('Runtime.evaluate', {
    expression,
    awaitPromise: true,
    returnByValue: true,
  })
  if (response.exceptionDetails) {
    throw new Error(response.exceptionDetails.exception?.description ?? '浏览器脚本执行失败')
  }
  return response.result.value
}

const performanceCollectorSource = `
  (() => {
    const state = {
      frames: [],
      longTasks: [],
      maxEventDomNodes: 0,
    }
    const eventNodeCount = () => {
      const nodes = new Set()
      for (const root of document.querySelectorAll(
        '.timeline__event-position, .timeline__cluster-position, .timeline__overlap-list li'
      )) {
        nodes.add(root)
        for (const descendant of root.querySelectorAll('*')) nodes.add(descendant)
      }
      return nodes.size
    }
    const sampleDom = () => {
      state.maxEventDomNodes = Math.max(state.maxEventDomNodes, eventNodeCount())
    }
    new MutationObserver(sampleDom).observe(document, { childList: true, subtree: true })
    if (typeof PerformanceObserver === 'function') {
      try {
        new PerformanceObserver((list) => {
          state.longTasks.push(...list.getEntries().map((entry) => ({
            startTime: entry.startTime,
            duration: entry.duration,
          })))
        }).observe({ type: 'longtask', buffered: true })
      } catch {}
    }
    const frame = (timestamp) => {
      state.frames.push(timestamp)
      if (state.frames.length > 5000) state.frames.shift()
      requestAnimationFrame(frame)
    }
    requestAnimationFrame(frame)
    window.__historyWikiPerformance = {
      eventNodeCount,
      reset() {
        state.frames.length = 0
        state.longTasks.length = 0
        state.maxEventDomNodes = eventNodeCount()
      },
      summary(startedAt) {
        sampleDom()
        const endedAt = performance.now()
        const frames = state.frames.filter((timestamp) => timestamp >= startedAt && timestamp <= endedAt)
        const durationSeconds = Math.max((endedAt - startedAt) / 1000, 0.001)
        const intervals = frames.slice(1).map((timestamp, index) => timestamp - frames[index]).sort((a, b) => a - b)
        const longTasks = state.longTasks.filter((entry) => entry.startTime + entry.duration >= startedAt)
        return {
          durationMs: endedAt - startedAt,
          fps: frames.length / durationSeconds,
          frameIntervalP95Ms: intervals[Math.min(intervals.length - 1, Math.floor(intervals.length * 0.95))] ?? 0,
          longTaskCount: longTasks.length,
          sustainedLongTaskCount: longTasks.filter((entry) => entry.duration >= 100).length,
          longestTaskMs: Math.max(0, ...longTasks.map((entry) => entry.duration)),
          maxEventDomNodes: state.maxEventDomNodes,
        }
      },
    }
  })()
`

class CdpSession {
  constructor(url) {
    this.url = url
    this.nextId = 1
    this.pending = new Map()
  }

  async open() {
    this.socket = new WebSocket(this.url)
    this.socket.addEventListener('message', (event) => {
      const message = JSON.parse(event.data)
      if (!message.id) return
      const pending = this.pending.get(message.id)
      if (!pending) return
      this.pending.delete(message.id)
      if (message.error) pending.reject(new Error(message.error.message))
      else pending.resolve(message.result)
    })
    await new Promise((resolve, reject) => {
      this.socket.addEventListener('open', resolve, { once: true })
      this.socket.addEventListener('error', reject, { once: true })
    })
  }

  send(method, params = {}) {
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.socket.send(JSON.stringify({ id, method, params }))
    })
  }

  close() {
    this.socket?.close()
  }
}

async function findChrome() {
  const candidates = [
    process.env.CHROME_PATH,
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/usr/bin/google-chrome',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
  ].filter(Boolean)
  for (const candidate of candidates) {
    try {
      await access(candidate)
      return candidate
    } catch {}
  }
  throw new Error('未找到 Chrome；可通过 CHROME_PATH 指定浏览器路径。')
}

async function availablePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer()
    server.unref()
    server.on('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      const port = typeof address === 'object' && address ? address.port : 0
      server.close(() => resolve(port))
    })
  })
}

async function waitForHttp(url) {
  const deadline = Date.now() + 10_000
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url)
      if (response.ok) return
    } catch {}
    await delay(100)
  }
  throw new Error(`服务未能及时启动：${url}`)
}

async function fetchJson(url, init) {
  const response = await fetch(url, init)
  if (!response.ok) throw new Error(`请求失败：${response.status} ${url}`)
  return response.json()
}

async function stopProcess(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return
  const exited = new Promise((resolve) => child.once('exit', resolve))
  child.kill('SIGTERM')
  await Promise.race([exited, delay(2_000)])
}

await main()
