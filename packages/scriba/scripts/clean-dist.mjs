import { rm } from 'node:fs/promises'
import { join } from 'node:path'

await rm(join(import.meta.dirname, '..', 'dist'), { recursive: true, force: true })
