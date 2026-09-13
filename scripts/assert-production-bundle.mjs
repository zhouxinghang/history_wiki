import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'

const assetDirectory = path.join(process.cwd(), 'dist', 'assets')
const files = await readdir(assetDirectory)
const javascript = (
  await Promise.all(
    files
      .filter((name) => name.endsWith('.js'))
      .map((name) => readFile(path.join(assetDirectory, name), 'utf8')),
  )
).join('\n')

for (const forbidden of ['吉萨大金字塔建成', '确定性性能测试摘要', 'demo=performance']) {
  if (javascript.includes(forbidden)) {
    throw new Error(`production bundle contains test fixture marker: ${forbidden}`)
  }
}
