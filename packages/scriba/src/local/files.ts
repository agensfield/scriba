import { readdir, stat } from 'node:fs/promises'
import { join } from 'node:path'

export async function* walkJsonlFiles(dir: string): AsyncGenerator<string> {
	const entries = await readdir(dir, { withFileTypes: true })
	for (const entry of entries) {
		const fullPath = join(dir, entry.name)
		if (entry.isDirectory()) {
			yield* walkJsonlFiles(fullPath)
		} else if (entry.isFile() && entry.name.endsWith('.jsonl')) {
			yield fullPath
		}
	}
}

export async function fileSize(path: string): Promise<number> {
	return (await stat(path)).size
}

export async function fileFingerprint(path: string): Promise<{ size: number; mtimeMs: number }> {
	const result = await stat(path)
	return { size: result.size, mtimeMs: result.mtimeMs }
}

export async function isDirectory(path: string): Promise<boolean> {
	try {
		return (await stat(path)).isDirectory()
	} catch {
		return false
	}
}
