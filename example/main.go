package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/unxed/colorer4go"
)

func main() {
	ctx := context.Background()

	// Автоматически определяем путь к оригинальным конфигам в репозитории
	configDirOnHost := "colorer/configs"
	if _, err := os.Stat(configDirOnHost); err != nil {
		// Если пример запущен из самой папки example/
		configDirOnHost = "../colorer/configs"
	}

	absConfigDir, err := filepath.Abs(configDirOnHost)
	if err != nil {
		fmt.Printf("Ошибка получения абсолютного пути: %v\n", err)
		os.Exit(1)
	}

	catalogPath := filepath.Join(absConfigDir, "base", "catalog.xml")
	if _, err := os.Stat(catalogPath); err != nil {
		fmt.Printf("Ошибка: оригинальные схемы Colorer не найдены по пути %s.\nУбедитесь, что репозиторий склонирован полностью.\n", catalogPath)
		os.Exit(1)
	}

	fmt.Printf("Инициализируем Colorer с оригинальными схемами из: %s\n", absConfigDir)

	// Создаем сессию, используя оригинальные схемы репозитория
	session, err := colorer.NewSession(ctx, "/base/catalog.xml", absConfigDir)
	if err != nil {
		fmt.Printf("Не удалось создать сессию: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	fmt.Println("Выбираем тип файла JSON...")
	success, err := session.SelectType("test.json", "{")
	if err != nil || !success {
		fmt.Printf("Не удалось выбрать тип файла: %v, успех: %v\n", err, success)
		os.Exit(1)
	}

	line := `{"key": "value"}`
	fmt.Printf("Парсим строку: %s\n", line)
	regions, err := session.ParseLine(line)
	if err != nil {
		fmt.Printf("Ошибка парсинга: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nУспех! Обнаружено %d регионов подсветки:\n", len(regions))
	for _, r := range regions {
		fmt.Printf("  [%d..%d]: %s\n", r.Start, r.End, r.Name)
	}
}