import { chmod, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

const cliPath = join(import.meta.dirname, '..', 'dist', 'cli.js')
const cli = await readFile(cliPath, 'utf8')
const withoutBunShebang = cli.replace(/^#!\/usr\/bin\/env bun\n/, '')

await writeFile(cliPath, `#!/usr/bin/env node\n${withoutBunShebang}`)
await chmod(cliPath, 0o755)
