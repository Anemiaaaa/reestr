// Package web вкладывает файлы интерфейса в бинарник.
//
// Отдельный пакет, а не каталог рядом с main: директива embed видит только свой
// каталог и вложенные в него, а интерфейс должен лежать на виду в корне
// репозитория, а не внутри cmd.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html login.html app.css app.js
var files embed.FS

// Files отдаёт файлы интерфейса из бинарника.
func Files() fs.FS { return files }
