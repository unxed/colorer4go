package colorer

import (
	"context"
	"testing"
)

func TestColorerWasm(t *testing.T) {
	ctx := context.Background()

	// Путь внутри песочницы: "/base/catalog.xml"
	// Локальная папка с конфигами: "colorer/configs"
	// Рантайм инициализируется автоматически внутри NewSession!
	session, err := NewSession(ctx, "/base/catalog.xml", "colorer/configs")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	// Выбираем тип файла JSON по имени и первому символу
	success, err := session.SelectType("test.json", "{")
	if err != nil || !success {
		t.Fatalf("Failed to select type: %v, success: %v", err, success)
	}

	// Парсим строку JSON
	regions, err := session.ParseLine(`{"key": "value"}`)
	if err != nil {
		t.Fatalf("Failed to parse line: %v", err)
	}

	t.Logf("Parsed %d regions:", len(regions))
	for _, r := range regions {
		t.Logf("  [%d..%d]: %s", r.Start, r.End, r.Name)
	}

	if len(regions) == 0 {
		t.Error("Expected to find some regions, but got 0")
	}
}