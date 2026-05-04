import { createReadStream } from 'node:fs'
import { createInterface } from 'node:readline'

export type JsonlLine = {
	line: string
	lineNumber: number
}

export async function* readJsonlLines(filePath: string): AsyncGenerator<JsonlLine> {
	const stream = createReadStream(filePath, { encoding: 'utf8' })
	const rl = createInterface({
		input: stream,
		crlfDelay: Number.POSITIVE_INFINITY,
	})

	let lineNumber = 0
	for await (const line of rl) {
		lineNumber += 1
		const trimmed = line.trim()
		if (trimmed === '') {
			continue
		}
		yield { line: trimmed, lineNumber }
	}
}
