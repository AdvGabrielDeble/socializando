package main

import (
    "testing"
)

func TestCPFValidation(t *testing.T) {
    for _, cpf := range []string{"52998224725", "16899535009", "11144477735"} {
        if !cpfValid(cpf) { t.Fatalf("CPF válido rejeitado: %s", cpf) }
    }
    if cpfValid("12345678901") { t.Fatal("CPF inválido aceito") }
    for i := 1; i <= 30; i++ {
        cpf := validCPF(i)
        if !cpfValid(cpf) { t.Fatalf("gerador produziu CPF inválido seed=%d cpf=%s", i, cpf) }
    }
}

func TestJulyParserContract(t *testing.T) {
    raw, err := syntheticJulyWorkbook()
    if err != nil { t.Fatal(err) }
    p, err := parseWorkbook(raw)
    if err != nil { t.Fatal(err) }
    firstReason := ""
    if len(p.Restricted) > 0 { firstReason = p.Restricted[0].Reason }
    if len(p.Valid) != 26 { t.Fatalf("valid=%d skipped=%d restricted=%d structural=%d first_reason=%q cpf1=%s", len(p.Valid), len(p.Skipped), len(p.Restricted), len(p.Structural), firstReason, validCPF(1)) }
    if len(p.Skipped) != 4 { t.Fatalf("skipped=%d", len(p.Skipped)) }
    if len(p.Restricted) != 0 { t.Fatalf("restricted=%d", len(p.Restricted)) }
    if len(p.Structural) != 0 { t.Fatalf("structural=%d", len(p.Structural)) }
    var total int64
    for _, r := range p.Valid { total += r.AmountCents }
    if total != 1087000 { t.Fatalf("total=%d", total) }
}

func TestInvalidCPFWithMovementBecomesRestriction(t *testing.T) {
    raw, err := singleRowWorkbook("Paciente", "12345678901", 10, 250)
    if err != nil { t.Fatal(err) }
    p, err := parseWorkbook(raw)
    if err != nil { t.Fatal(err) }
    if len(p.Valid) != 0 || len(p.Restricted) != 1 || len(p.Skipped) != 0 {
        t.Fatalf("valid=%d restricted=%d skipped=%d", len(p.Valid), len(p.Restricted), len(p.Skipped))
    }
}

func TestPersistenceIsolationDuplicateAndExport(t *testing.T) {
    root := t.TempDir()
    a, err := newApp(root)
    if err != nil { t.Fatal(err) }
    closed := false
    defer func(){ if !closed { _ = a.db.Close() } }()
    idA, err := a.addProfessional("Profissional A", "52998224725", "PSICOLOGO", "07/10000", "RS")
    if err != nil { t.Fatal(err) }
    raw, _ := syntheticJulyWorkbook()
    p, err := parseWorkbook(raw)
    if err != nil { t.Fatal(err) }
    p.SourceName = "julho.xlsx"
    created, restricted, duplicates, err := a.importPreview(p, idA)
    if err != nil { t.Fatal(err) }
    if created != 26 || restricted != 0 || duplicates != 0 { t.Fatalf("first import=%d/%d/%d", created, restricted, duplicates) }
    created, restricted, duplicates, err = a.importPreview(p, idA)
    if err != nil { t.Fatal(err) }
    if created != 0 || restricted != 0 || duplicates != 26 { t.Fatalf("duplicate import=%d/%d/%d", created, restricted, duplicates) }
    if err := a.db.Close(); err != nil { t.Fatal(err) }
    closed = true

    a, err = newApp(root)
    if err != nil { t.Fatal(err) }
    closed = false
    defer func(){ if !closed { _ = a.db.Close() } }()
    c, total, err := countAndTotal(a.db, idA)
    if err != nil || c != 26 || total != 1087000 { t.Fatalf("persistence=%d/%d/%v", c, total, err) }

    idB, err := a.addProfessional("Profissional B", "16899535009", "PSICOLOGO", "07/20000", "RS")
    if err != nil { t.Fatal(err) }
    braw, _ := singleRowWorkbook("Paciente B", "11144477735", 20, 333)
    bp, err := parseWorkbook(braw)
    if err != nil { t.Fatal(err) }
    bp.SourceName = "b.xlsx"
    created, _, _, err = a.importPreview(bp, idB)
    if err != nil || created != 1 { t.Fatalf("B import=%d %v", created, err) }
    ca, ta, _ := countAndTotal(a.db, idA)
    cb, tb, _ := countAndTotal(a.db, idB)
    if ca != 26 || ta != 1087000 || cb != 1 || tb != 33300 { t.Fatalf("isolation A=%d/%d B=%d/%d", ca, ta, cb, tb) }

    if err := a.validateAll(idA, 7, 2026); err != nil { t.Fatal(err) }
    rows, exportTotal, err := exportRowsForTest(a.db, idA)
    if err != nil { t.Fatal(err) }
    if len(rows) != 26 || exportTotal != 1087000 { t.Fatalf("export=%d/%d", len(rows), exportTotal) }
    for _, row := range rows { if len(row) != 16 || row[14] != "52998224725" { t.Fatal("CSV misturou profissional ou não tem 16 campos") } }
    _ = a.db.Close(); closed = true
}
