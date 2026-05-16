package local

import (
	"bufio"
	"os"
)

func ReadJSONLLines(path string, fn func(line []byte)) error {
	file, err := os.Open(path) // #nosec G304 -- Scriba intentionally reads user-configured local usage log paths.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 64*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		fn(line)
	}
	return scanner.Err()
}
