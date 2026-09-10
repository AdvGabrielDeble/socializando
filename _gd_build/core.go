package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	AppName       = "GD Fiscal Saúde"
	AppVersion    = "1.0.0-mvp"
	IncomeCode    = "R01.001.001"
	ReceivedFrom  = "PF"
	ReceiptMarker = "S"
)

var occupationLabels = map[string]string{
	"225": "Médico",
	"226": "Odontólogo/Dentista",
	"230": "Fonoaudiólogo",
	"231": "Fisioterapeuta",
	"232": "Terapeuta Ocupacional",
	"255": "Psicólogo",
}

var monthNames = map[string]int{
	"janeiro": 1,
	"fevereiro": 2,
	"marco": 3,
	"abril": 4,
	"maio": 5,
	"junho": 6,
	"julho": 7,
	"agosto": 8,
	"setembro": 9,
	"outubro": 10,
	"novembro": 11,
	"dezembro": 12,
}

var yearRx = regexp.MustCompile(`\b(20[0-9]{2})\b`)
var cpfCandidateRx = regexp.MustCompile(`[0-9][0-9 .-]{8,24}[0-9]`)

type Professional struct {
	ID             int64
	Name           string
	CPF            string
	OccupationCode string
	Registry       string
	Active         bool
}

type Payment struct {
	ID              int64
	ProfessionalID  int64
	Professional    string
	PayerName       string
	PayerCPF        string
	BeneficiaryName string
	BeneficiaryCPF  string
	PaymentDate     string
	AmountCents     int64
	Description     string
	Status          string
	DuplicateKey    string
}

type Restriction struct {
	ID                 int64
	ProfessionalID     int64
	Professional       string
	SourceName          string
	SourceSheet         string
	SourceRow           int
	CompetenceMonth     int
	CompetenceYear      int
	PayerName           string
	PayerCPFRaw         string
	BeneficiaryName     string
	BeneficiaryCPFRaw   string
	PaymentDateRaw      string
	AmountRaw           string
	IssueCode           string
	IssueReason         string
	Status              string
}

type ParsedRecord struct {
	SourceSheet     string
	SourceRow       int
	CompetenceMonth int
	CompetenceYear  int
	PayerName       string
	PayerCPF        string
	BeneficiaryName string
	BeneficiaryCPF  string
	PaymentDate     string
	AmountCents     int64
}

type SkippedRow struct {
	SourceSheet     string
	SourceRow       int
	CompetenceMonth int
	CompetenceYear  int
	Reason          string
	Content         string
}

type RestrictedRow struct {
	SourceSheet        string
	SourceRow          int
	CompetenceMonth    int
	CompetenceYear     int
	PayerName          string
	PayerCPFRaw        string
	BeneficiaryName    string
	BeneficiaryCPFRaw  string
	PaymentDateRaw     string
	AmountRaw          string
	IssueCode          string
	IssueReason        string
	Content            string
}

type Preview struct {
	SourceName  string
	Valid       []ParsedRecord
	Skipped     []SkippedRow
	Restricted  []RestrictedRow
	Errors      []string
	Blocks      int
	Sheets      int
}

type ImportStats struct {
	CreatedPayments     int
	CreatedRestrictions int
	SkippedNoService    int
	SkippedDuplicates   int
}

type PaymentFilter struct {
	ProfessionalID int64
	Year           int
	Month          int
	Status         string
}

type Store interface {
	Close() error
	AddProfessional(Professional) (int64, error)
	UpdateProfessional(Professional) error
	GetProfessional(int64) (Professional, error)
	ListProfessionals() ([]Professional, error)
	ImportPreview(int64, Preview) (ImportStats, error)
	ListPayments(PaymentFilter) ([]Payment, error)
	ValidatePayments(int64, int, int) (int, error)
	ListRestrictions() ([]Restriction, error)
	ResolveRestriction(int64, string, string, string, string, string, string) (int64, error)
	MarkExported([]int64) error
	Counts() (map[string]int, error)
}

type rawCell struct {
	Text    string
	Numeric bool
}

type sheetData struct {
	Title string
	Rows  [][]rawCell
}

type block struct {
	HeaderRow      int
	StartCol       int
	EndCol         int
	NameCol        int
	PayerCol       int
	BeneficiaryCol int
	DateCol        int
	AmountCol      int
	Month          int
	Year           int
}

func normalizeText(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	replacer := strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ç", "c",
	)
	return strings.Join(strings.Fields(replacer.Replace(s)), " ")
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func cpfValid(cpf string) bool {
	cpf = onlyDigits(cpf)
	if len(cpf) != 11 {
		return false
	}
	allSame := true
	for i := 1; i < 11; i++ {
		if cpf[i] != cpf[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return false
	}
	for length := 9; length <= 10; length++ {
		total := 0
		for i := 0; i < length; i++ {
			total += int(cpf[i]-'0') * (length + 1 - i)
		}
		digit := (total * 10) % 11
		if digit == 10 {
			digit = 0
		}
		if digit != int(cpf[length]-'0') {
			return false
		}
	}
	return true
}

func normalizeCPF(s string) (string, error) {
	cpf := onlyDigits(s)
	if !cpfValid(cpf) {
		return "", errors.New("CPF inválido")
	}
	return cpf, nil
}

func normalizeCPFCell(c rawCell) (string, error) {
	d := onlyDigits(c.Text)
	if len(d) == 11 && cpfValid(d) {
		return d, nil
	}
	if c.Numeric && len(d) == 10 {
		candidate := "0" + d
		if cpfValid(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("CPF ausente ou inválido")
}

func cleanRegistry(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 15 {
		return "", errors.New("registro profissional excede 15 caracteres")
	}
	if strings.ContainsAny(s, ";\r\n\"") {
		return "", errors.New("registro profissional contém caractere incompatível")
	}
	return s, nil
}

func validateProfessional(p Professional) (Professional, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, errors.New("nome do profissional é obrigatório")
	}
	cpf, err := normalizeCPF(p.CPF)
	if err != nil {
		return p, fmt.Errorf("CPF do profissional inválido")
	}
	p.CPF = cpf
	if _, ok := occupationLabels[p.OccupationCode]; !ok {
		return p, errors.New("ocupação profissional inválida")
	}
	p.Registry, err = cleanRegistry(p.Registry)
	if err != nil {
		return p, err
	}
	p.Active = true
	return p, nil
}

func cell(rows [][]rawCell, r, c int) rawCell {
	if r < 0 || r >= len(rows) || c < 0 || c >= len(rows[r]) {
		return rawCell{}
	}
	return rows[r][c]
}

func isBlank(c rawCell) bool { return strings.TrimSpace(c.Text) == "" }

func rowSegmentHasData(row []rawCell, start, end int) bool {
	if start < 0 {
		start = 0
	}
	if end > len(row) {
		end = len(row)
	}
	for i := start; i < end; i++ {
		if strings.TrimSpace(row[i].Text) != "" {
			return true
		}
	}
	return false
}

func monthFromText(s string) int {
	n := normalizeText(s)
	for name, m := range monthNames {
		if strings.Contains(n, name) {
			return m
		}
	}
	return 0
}

func yearFromText(s string) int {
	m := yearRx.FindStringSubmatch(s)
	if len(m) != 2 {
		return 0
	}
	y, _ := strconv.Atoi(m[1])
	return y
}

func globalYear(rows [][]rawCell, title string) int {
	if y := yearFromText(title); y != 0 {
		return y
	}
	limit := len(rows)
	if limit > 20 {
		limit = 20
	}
	for r := 0; r < limit; r++ {
		for _, c := range rows[r] {
			if y := yearFromText(c.Text); y != 0 {
				return y
			}
		}
	}
	return 0
}

func payerHeaderColumns(row []rawCell) []int {
	var out []int
	for i, c := range row {
		t := normalizeText(c.Text)
		if strings.Contains(t, "cpf") && (strings.Contains(t, "responsavel") || strings.Contains(t, "pagador")) {
			out = append(out, i)
		}
	}
	return out
}

func isNameHeader(s string) bool {
	t := normalizeText(s)
	return t == "nome" || (strings.Contains(t, "nome") && (strings.Contains(t, "responsavel") || strings.Contains(t, "pagador")))
}

func findNameColumn(row []rawCell, payer int) int {
	start := payer - 10
	if start < 0 {
		start = 0
	}
	for i := payer - 1; i >= start; i-- {
		if isNameHeader(row[i].Text) {
			return i
		}
	}
	end := payer + 11
	if end > len(row) {
		end = len(row)
	}
	for i := payer + 1; i < end; i++ {
		if isNameHeader(row[i].Text) {
			return i
		}
	}
	return -1
}

func findColumn(row []rawCell, start, end int, required []string, forbidden []string) int {
	if start < 0 {
		start = 0
	}
	if end > len(row) {
		end = len(row)
	}
	for i := start; i < end; i++ {
		t := normalizeText(row[i].Text)
		ok := true
		for _, term := range required {
			if !strings.Contains(t, term) {
				ok = false
				break
			}
		}
		for _, term := range forbidden {
			if strings.Contains(t, term) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func findBeneficiaryColumn(row []rawCell, start, end int) int {
	if start < 0 {
		start = 0
	}
	if end > len(row) {
		end = len(row)
	}
	for i := start; i < end; i++ {
		t := normalizeText(row[i].Text)
		if strings.Contains(t, "paciente") || strings.Contains(t, "beneficiario") {
			return i
		}
	}
	return -1
}

func blockContext(rows [][]rawCell, headerRow, start, end int, title string, year int) (int, int) {
	for r := headerRow - 1; r >= 0 && r >= headerRow-4; r-- {
		limit := end
		if limit > len(rows[r]) {
			limit = len(rows[r])
		}
		for c := start; c < limit; c++ {
			if m := monthFromText(rows[r][c].Text); m != 0 {
				y := yearFromText(rows[r][c].Text)
				if y == 0 {
					y = year
				}
				return m, y
			}
		}
	}
	if m := monthFromText(title); m != 0 {
		y := yearFromText(title)
		if y == 0 {
			y = year
		}
		return m, y
	}
	return 0, year
}

func detectBlocks(rows [][]rawCell, title string) []block {
	year := globalYear(rows, title)
	var blocks []block
	for ri, row := range rows {
		payers := payerHeaderColumns(row)
		if len(payers) == 0 {
			continue
		}
		names := make([]int, len(payers))
		for i, p := range payers {
			names[i] = findNameColumn(row, p)
		}
		for pos, p := range payers {
			start, end := 0, len(row)
			if len(payers) > 1 {
				if names[pos] >= 0 && names[pos] < p {
					start = names[pos]
				} else {
					start = p - 10
					if start < 0 {
						start = 0
					}
				}
				if pos+1 < len(payers) {
					if names[pos+1] > p {
						end = names[pos+1]
					} else {
						end = payers[pos+1]
					}
				}
			}
			beneficiary := findBeneficiaryColumn(row, start, end)
			dateCol := findColumn(row, start, end, []string{"data"}, []string{"nascimento"})
			amountCol := findColumn(row, start, end, []string{"valor"}, nil)
			if dateCol < 0 || amountCol < 0 {
				continue
			}
			month, blockYear := blockContext(rows, ri, start, end, title, year)
			blocks = append(blocks, block{
				HeaderRow: ri, StartCol: start, EndCol: end, NameCol: names[pos], PayerCol: p,
				BeneficiaryCol: beneficiary, DateCol: dateCol, AmountCol: amountCol, Month: month, Year: blockYear,
			})
		}
	}
	return blocks
}

func isTotalRow(row []rawCell, b block) bool {
	end := b.EndCol
	if end > len(row) {
		end = len(row)
	}
	for c := b.StartCol; c < end; c++ {
		if strings.Contains(normalizeText(row[c].Text), "valor total mes") {
			return true
		}
	}
	return false
}

func isRepeatedHeader(row []rawCell, b block) bool {
	if b.PayerCol < 0 || b.PayerCol >= len(row) {
		return false
	}
	t := normalizeText(row[b.PayerCol].Text)
	return strings.Contains(t, "cpf") && (strings.Contains(t, "responsavel") || strings.Contains(t, "pagador"))
}

func excelSerialDate(v float64) (time.Time, error) {
	if v <= 0 || v > 1000000 {
		return time.Time{}, errors.New("data numérica inválida")
	}
	whole := int(math.Floor(v + 1e-9))
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, whole), nil
}

func parseDateCell(c rawCell, month, year int) (string, error) {
	raw := strings.TrimSpace(c.Text)
	if raw == "" {
		return "", errors.New("data ausente")
	}
	if c.Numeric {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			if f >= 1 && f <= 31 && month >= 1 && month <= 12 && year >= 2000 {
				d := int(f)
				t := time.Date(year, time.Month(month), d, 0, 0, 0, 0, time.UTC)
				if t.Month() != time.Month(month) || t.Day() != d {
					return "", errors.New("data inválida")
				}
				return t.Format("2006-01-02"), nil
			}
			if f > 31 {
				t, err := excelSerialDate(f)
				if err == nil {
					return t.Format("2006-01-02"), nil
				}
			}
		}
	}
	layouts := []string{"2006-01-02", "02/01/2006", "02/01/06", "2/1/2006", "2/1/06"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t.Format("2006-01-02"), nil
		}
	}
	if d, err := strconv.Atoi(raw); err == nil && d >= 1 && d <= 31 && month >= 1 && month <= 12 && year >= 2000 {
		t := time.Date(year, time.Month(month), d, 0, 0, 0, 0, time.UTC)
		if t.Month() == time.Month(month) && t.Day() == d {
			return t.Format("2006-01-02"), nil
		}
	}
	return "", errors.New("data não reconhecida")
}

func parseAmountCell(c rawCell) (int64, error) {
	raw := strings.TrimSpace(c.Text)
	if raw == "" {
		return 0, errors.New("valor ausente")
	}
	s := strings.ReplaceAll(raw, "R$", "")
	s = strings.ReplaceAll(s, " ", "")
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	} else if strings.Count(s, ".") > 1 {
		s = strings.ReplaceAll(s, ".", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("valor inválido")
	}
	if f <= 0 {
		return 0, errors.New("valor deve ser maior que zero")
	}
	if f > 99999999999.99 {
		return 0, errors.New("valor excede 99999999999,99")
	}
	return int64(math.Round(f * 100)), nil
}

func extractCPFName(c rawCell) (string, string) {
	text := strings.TrimSpace(c.Text)
	for _, match := range cpfCandidateRx.FindAllString(text, -1) {
		d := onlyDigits(match)
		if len(d) == 11 && cpfValid(d) {
			name := strings.TrimSpace(strings.Replace(text, match, "", 1))
			name = strings.Trim(name, " -–—;,:/")
			return d, strings.Join(strings.Fields(name), " ")
		}
	}
	d := onlyDigits(text)
	if len(d) == 11 && cpfValid(d) {
		return d, ""
	}
	if c.Numeric && len(d) == 10 {
		candidate := "0" + d
		if cpfValid(candidate) {
			return candidate, ""
		}
	}
	return "", text
}

func issueCode(reason string) string {
	n := normalizeText(reason)
	switch {
	case strings.Contains(n, "cpf"):
		return "CPF_INVALIDO"
	case strings.Contains(n, "data"):
		return "DATA_INVALIDA"
	case strings.Contains(n, "valor"):
		return "VALOR_INVALIDO"
	case strings.Contains(n, "nome"):
		return "NOME_AUSENTE"
	default:
		return "DADOS_INVALIDOS"
	}
}

func rowContent(row []rawCell, start, end int) string {
	if end > len(row) {
		end = len(row)
	}
	var parts []string
	for i := start; i < end; i++ {
		if s := strings.TrimSpace(row[i].Text); s != "" {
			parts = append(parts, s)
		}
	}
	out := strings.Join(parts, " | ")
	if len(out) > 300 {
		out = out[:300]
	}
	return out
}

func classifyRow(sheet string, sourceRow int, row []rawCell, b block) (ParsedRecord, *SkippedRow, *RestrictedRow) {
	dateCell := cell([][]rawCell{row}, 0, b.DateCol)
	amountCell := cell([][]rawCell{row}, 0, b.AmountCol)
	content := rowContent(row, b.StartCol, b.EndCol)
	if isBlank(dateCell) && isBlank(amountCell) {
		return ParsedRecord{}, &SkippedRow{SourceSheet: sheet, SourceRow: sourceRow, CompetenceMonth: b.Month, CompetenceYear: b.Year, Reason: "Sem atendimento no período", Content: content}, nil
	}

	payerName := ""
	if b.NameCol >= 0 && b.NameCol < len(row) {
		payerName = strings.TrimSpace(row[b.NameCol].Text)
	}
	payerRaw := cell([][]rawCell{row}, 0, b.PayerCol)
	payerCPF, payerErr := normalizeCPFCell(payerRaw)
	beneficiaryName, beneficiaryCPF := payerName, payerCPF
	var problems []string
	if payerErr != nil {
		problems = append(problems, "CPF do responsável ausente ou inválido")
	}
	if payerName == "" {
		problems = append(problems, "nome do responsável ausente")
	}

	if b.BeneficiaryCol >= 0 && b.BeneficiaryCol < len(row) && !isBlank(row[b.BeneficiaryCol]) {
		beneficiaryCPF, beneficiaryName = extractCPFName(row[b.BeneficiaryCol])
		if beneficiaryCPF == "" || !cpfValid(beneficiaryCPF) {
			problems = append(problems, "CPF do paciente/beneficiário ausente ou inválido")
		}
		if strings.TrimSpace(beneficiaryName) == "" {
			problems = append(problems, "nome do paciente/beneficiário ausente")
		}
	}

	paymentDate, dateErr := parseDateCell(dateCell, b.Month, b.Year)
	if dateErr != nil {
		problems = append(problems, dateErr.Error())
	}
	amountCents, amountErr := parseAmountCell(amountCell)
	if amountErr != nil {
		problems = append(problems, amountErr.Error())
	}

	if len(problems) > 0 {
		reason := strings.Join(problems, "; ")
		return ParsedRecord{}, nil, &RestrictedRow{
			SourceSheet: sheet, SourceRow: sourceRow, CompetenceMonth: b.Month, CompetenceYear: b.Year,
			PayerName: payerName, PayerCPFRaw: strings.TrimSpace(payerRaw.Text), BeneficiaryName: beneficiaryName,
			BeneficiaryCPFRaw: beneficiaryCPF, PaymentDateRaw: strings.TrimSpace(dateCell.Text), AmountRaw: strings.TrimSpace(amountCell.Text),
			IssueCode: issueCode(reason), IssueReason: reason, Content: content,
		}
	}

	return ParsedRecord{
		SourceSheet: sheet, SourceRow: sourceRow, CompetenceMonth: b.Month, CompetenceYear: b.Year,
		PayerName: payerName, PayerCPF: payerCPF, BeneficiaryName: beneficiaryName, BeneficiaryCPF: beneficiaryCPF,
		PaymentDate: paymentDate, AmountCents: amountCents,
	}, nil, nil
}

func parseWorkbook(sourceName string, raw []byte) (Preview, error) {
	preview := Preview{SourceName: sourceName}
	sheets, err := readXLSX(raw)
	if err != nil {
		return preview, err
	}
	preview.Sheets = len(sheets)
	for _, sheet := range sheets {
		blocks := detectBlocks(sheet.Rows, sheet.Title)
		preview.Blocks += len(blocks)
		for _, b := range blocks {
			for ri := b.HeaderRow + 1; ri < len(sheet.Rows); ri++ {
				row := sheet.Rows[ri]
				if isTotalRow(row, b) || isRepeatedHeader(row, b) {
					break
				}
				if !rowSegmentHasData(row, b.StartCol, b.EndCol) {
					continue
				}
				record, skipped, restricted := classifyRow(sheet.Title, ri+1, row, b)
				if skipped != nil {
					preview.Skipped = append(preview.Skipped, *skipped)
				} else if restricted != nil {
					preview.Restricted = append(preview.Restricted, *restricted)
				} else {
					preview.Valid = append(preview.Valid, record)
				}
			}
		}
	}
	if preview.Blocks == 0 {
		preview.Errors = append(preview.Errors, "Nenhum bloco reconhecido. A planilha deve possuir colunas de Nome, CPF do Responsável/Pagador, Data e Valor.")
	}
	return preview, nil
}

func parseWorkbookFileName(name string, raw []byte) (Preview, error) {
	if !strings.EqualFold(path.Ext(name), ".xlsx") {
		return Preview{}, errors.New("formato não suportado; selecione um arquivo .xlsx")
	}
	return parseWorkbook(name, raw)
}

func readXLSX(raw []byte) ([]sheetData, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, errors.New("arquivo XLSX inválido")
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.TrimPrefix(path.Clean(f.Name), "/")] = f
	}
	readFile := func(name string) ([]byte, error) {
		f := files[strings.TrimPrefix(path.Clean(name), "/")]
		if f == nil {
			return nil, fmt.Errorf("XLSX incompleto: %s ausente", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}

	var shared []string
	if f := files["xl/sharedStrings.xml"]; f != nil {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		shared, err = parseSharedStrings(data)
		if err != nil {
			return nil, err
		}
	}

	wbXML, err := readFile("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	relXML, err := readFile("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	sheets, err := parseWorkbookSheets(wbXML)
	if err != nil {
		return nil, err
	}
	rels, err := parseWorkbookRels(relXML)
	if err != nil {
		return nil, err
	}
	var out []sheetData
	for _, meta := range sheets {
		if meta.State != "" && !strings.EqualFold(meta.State, "visible") {
			continue
		}
		target := rels[meta.RelID]
		if target == "" {
			continue
		}
		target = strings.TrimPrefix(target, "/")
		if !strings.HasPrefix(target, "xl/") {
			target = path.Join("xl", target)
		}
		data, err := readFile(target)
		if err != nil {
			return nil, err
		}
		rows, err := parseWorksheet(data, shared)
		if err != nil {
			return nil, err
		}
		out = append(out, sheetData{Title: meta.Name, Rows: rows})
	}
	if len(out) == 0 {
		return nil, errors.New("XLSX sem abas visíveis legíveis")
	}
	return out, nil
}

type workbookSheetMeta struct {
	Name  string
	State string
	RelID string
}

func parseWorkbookSheets(data []byte) ([]workbookSheetMeta, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []workbookSheetMeta
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		var m workbookSheetMeta
		for _, a := range start.Attr {
			switch a.Name.Local {
			case "name":
				m.Name = a.Value
			case "state":
				m.State = a.Value
			case "id":
				m.RelID = a.Value
			}
		}
		if m.Name != "" && m.RelID != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

func parseWorkbookRels(data []byte) (map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	out := map[string]string{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		id, target := "", ""
		for _, a := range start.Attr {
			switch a.Name.Local {
			case "Id":
				id = a.Value
			case "Target":
				target = a.Value
			}
		}
		if id != "" && target != "" {
			out[id] = target
		}
	}
	return out, nil
}

func parseSharedStrings(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []string
	var current strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "si" {
				inSI = true
				current.Reset()
			} else if inSI && t.Name.Local == "t" {
				inT = true
			}
		case xml.CharData:
			if inSI && inT {
				current.Write([]byte(t))
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inT = false
			} else if t.Name.Local == "si" {
				out = append(out, current.String())
				inSI = false
			}
		}
	}
	return out, nil
}

func columnIndex(ref string) int {
	col := 0
	for i := 0; i < len(ref); i++ {
		ch := ref[i]
		if ch >= 'A' && ch <= 'Z' {
			col = col*26 + int(ch-'A'+1)
		} else if ch >= 'a' && ch <= 'z' {
			col = col*26 + int(ch-'a'+1)
		} else {
			break
		}
	}
	return col - 1
}

func parseWorksheet(data []byte, shared []string) ([][]rawCell, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	rows := map[int]map[int]rawCell{}
	maxRow, maxCol := -1, -1
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "c" {
			continue
		}
		ref, cellType := "", ""
		for _, a := range start.Attr {
			if a.Name.Local == "r" {
				ref = a.Value
			} else if a.Name.Local == "t" {
				cellType = a.Value
			}
		}
		col := columnIndex(ref)
		rowNum := 0
		for i := 0; i < len(ref); i++ {
			if ref[i] >= '0' && ref[i] <= '9' {
				rowNum, _ = strconv.Atoi(ref[i:])
				break
			}
		}
		if col < 0 || rowNum <= 0 {
			var discard any
			if err := dec.DecodeElement(&discard, &start); err != nil {
				return nil, err
			}
			continue
		}
		var cx struct {
			V  string `xml:"v"`
			IS struct {
				T string `xml:"t"`
				R []struct { T string `xml:"t"` } `xml:"r"`
			} `xml:"is"`
		}
		if err := dec.DecodeElement(&cx, &start); err != nil {
			return nil, err
		}
		text := cx.V
		numeric := cellType == "" || cellType == "n"
		switch cellType {
		case "s":
			idx, _ := strconv.Atoi(strings.TrimSpace(cx.V))
			if idx >= 0 && idx < len(shared) {
				text = shared[idx]
			}
			numeric = false
		case "inlineStr":
			text = cx.IS.T
			if text == "" {
				var b strings.Builder
				for _, run := range cx.IS.R {
					b.WriteString(run.T)
				}
				text = b.String()
			}
			numeric = false
		case "str":
			numeric = false
		}
		if rows[rowNum-1] == nil {
			rows[rowNum-1] = map[int]rawCell{}
		}
		rows[rowNum-1][col] = rawCell{Text: text, Numeric: numeric}
		if rowNum-1 > maxRow {
			maxRow = rowNum - 1
		}
		if col > maxCol {
			maxCol = col
		}
	}
	if maxRow < 0 {
		return [][]rawCell{}, nil
	}
	out := make([][]rawCell, maxRow+1)
	for r := 0; r <= maxRow; r++ {
		out[r] = make([]rawCell, maxCol+1)
		for c, v := range rows[r] {
			out[r][c] = v
		}
	}
	return out, nil
}

func duplicateKey(payerCPF, beneficiaryCPF, date string, amount int64) string {
	h := sha256.Sum256([]byte(strings.Join([]string{payerCPF, beneficiaryCPF, date, strconv.FormatInt(amount, 10)}, "\x1f")))
	return hex.EncodeToString(h[:])
}

func centsToReceita(cents int64) (string, error) {
	if cents <= 0 {
		return "", errors.New("valor deve ser maior que zero")
	}
	whole := cents / 100
	frac := cents % 100
	return fmt.Sprintf("%d,%02d", whole, frac), nil
}

func receitaCSV(prof Professional, payments []Payment) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	writer.Comma = ';'
	writer.UseCRLF = true
	for _, p := range payments {
		if p.Status != "VALIDADO_RECEITA_SAUDE" {
			return nil, fmt.Errorf("lançamento %d não está validado", p.ID)
		}
		if !cpfValid(p.PayerCPF) || !cpfValid(p.BeneficiaryCPF) || !cpfValid(prof.CPF) {
			return nil, errors.New("CPF inválido bloqueia a escrituração")
		}
		if _, ok := occupationLabels[prof.OccupationCode]; !ok {
			return nil, errors.New("ocupação incompatível com Receita Saúde")
		}
		t, err := time.Parse("2006-01-02", p.PaymentDate)
		if err != nil {
			return nil, errors.New("data de pagamento inválida")
		}
		amount, err := centsToReceita(p.AmountCents)
		if err != nil {
			return nil, err
		}
		description := strings.TrimSpace(p.Description)
		if description == "" || len([]rune(description)) > 255 || strings.ContainsAny(description, ";\r\n\"") {
			return nil, errors.New("descrição fiscal incompatível")
		}
		registry, err := cleanRegistry(prof.Registry)
		if err != nil {
			return nil, err
		}
		row := []string{
			t.Format("02/01/2006"), IncomeCode, prof.OccupationCode, amount, "", description,
			ReceivedFrom, p.PayerCPF, p.BeneficiaryCPF, "", "", "", "", ReceiptMarker, prof.CPF, registry,
		}
		if len(row) != 16 {
			return nil, errors.New("linha fiscal deve possuir 16 campos")
		}
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func previewCompetenceSummary(p Preview) []struct {
	Year int
	Month int
	Count int
	Amount int64
} {
	type key struct{ y, m int }
	m := map[key]struct{ count int; amount int64 }{}
	for _, r := range p.Valid {
		k := key{r.CompetenceYear, r.CompetenceMonth}
		v := m[k]
		v.count++
		v.amount += r.AmountCents
		m[k] = v
	}
	keys := make([]key, 0, len(m))
	for k := range m { keys = append(keys, k) }
	sort.Slice(keys, func(i, j int) bool { if keys[i].y == keys[j].y { return keys[i].m < keys[j].m }; return keys[i].y < keys[j].y })
	out := make([]struct { Year int; Month int; Count int; Amount int64 }, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		out = append(out, struct { Year int; Month int; Count int; Amount int64 }{k.y, k.m, v.count, v.amount})
	}
	return out
}

func formatMoney(cents int64) string {
	negative := cents < 0
	if negative { cents = -cents }
	whole := cents / 100
	frac := cents % 100
	s := strconv.FormatInt(whole, 10)
	for i := len(s)-3; i > 0; i -= 3 { s = s[:i] + "." + s[i:] }
	if negative { s = "-" + s }
	return fmt.Sprintf("R$ %s,%02d", s, frac)
}
