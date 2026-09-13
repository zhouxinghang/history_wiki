import { readFile, writeFile } from 'node:fs/promises'
import { parseYaml, stringifyYaml } from '@redocly/openapi-core'

const [source, destination] = process.argv.slice(2)
if (!source || !destination) {
  throw new Error('usage: openapi-for-oapi-codegen.mjs SOURCE DESTINATION')
}

const document = parseYaml(await readFile(source, 'utf8'))
document.openapi = '3.0.3'

function normalize(value) {
  if (Array.isArray(value)) {
    value.forEach(normalize)
    return
  }
  if (!value || typeof value !== 'object') return

	for (const [key, child] of Object.entries(value)) {
	  normalize(child)
	  if (Array.isArray(child?.oneOf)) {
	    const concrete = child.oneOf.filter((candidate) => candidate?.type !== 'null')
	    if (concrete.length !== child.oneOf.length) {
	      child.nullable = true
	      if (concrete.length === 1) {
	        delete child.oneOf
	        Object.assign(child, concrete[0])
	      } else child.oneOf = concrete
	    }
	  }
	  if (child?.type === 'null') value[key] = { type: 'string', nullable: true }
	}

  if (Array.isArray(value.type) && value.type.includes('null')) {
    const concrete = value.type.filter((type) => type !== 'null')
    if (concrete.length === 1) value.type = concrete[0]
    else {
      delete value.type
      value.oneOf = concrete.map((type) => ({ type }))
    }
    value.nullable = true
  }
  if (Object.hasOwn(value, 'const')) {
    value.enum = [value.const]
    delete value.const
  }
}

normalize(document)
await writeFile(destination, stringifyYaml(document))
