package main

import (
    "bytes"
    "crypto/rand"
    "database/sql"
    "encoding/csv"
    "encoding/hex"
    "errors"
    "fmt"
    "html/template"
    "io"
    "log"
    "net"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "runtime"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/xuri/excelize/v2"
    _ "modernc.org/sqlite"
)

const (
    appName       = "GD Fiscal Saúde"
    schemaVersion = 1
    incomeCode    = "R01.001.001"
)

var digitRE = regexp.MustCompile(`\D`)
var yearRE = regexp.MustCompile(`\b(20\d{2})\b`)

var occupationCodes = map[string]string{
    "MEDICO": "225",
    "ODONTOLOGO_DENTISTA": "226",
    "FONOAUDIOLOGO": "230",
    "FISIOTERAPEUTA": "231",
    "TERAPEUTA_OCUPACIONAL": "232",
    "PSICOLOGO": "255",
}

var professionLabels = map[string]string{
    "MEDICO": "Médico",
    "ODONTOLOGO_DENTISTA": "Odontólogo/Dentista",
    "FONOAUDIOLOGO": "Fonoaudiólogo",
    "FISIOTERAPEUTA": "Fisioterapeuta",
    "TERAPEUTA_OCUPACIONAL": "Terapeuta Ocupacional",
    "PSICOLOGO": "Psicólogo",
}

var monthNames = map[string]int{
    "janeiro": 1, "fevereiro": 2, "marco": 3, "abril": 4,
    "maio": 5, "junho": 6, "julho": 7, "agosto": 8,
    "setembro": 9, "outubro": 10, "novembro": 11, "dezembro": 12,
}

var monthLabels = []string{"", "Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho", "Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro"}

type Professional struct {
    ID            int64
    Name          string
    CPF           string
    ProfessionKey string
    Registry      string
    RegistryState string
    Active        bool
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
    Status          string
}

type ImportRow struct {
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

type IssueRow struct {
    SourceSheet     string
    SourceRow       int
    CompetenceMonth int
    CompetenceYear  int
    PayerName       string
    PayerCPFRaw     string
    BeneficiaryName string
    BeneficiaryRaw  string
    PaymentDateRaw  string
    AmountRaw       string
    Reason          string
}

type Preview struct {
    Token        string
    SourceName   string
    Valid        []ImportRow
    Skipped      []IssueRow
    Restricted   []IssueRow
    Structural   []IssueRow
    Competences  []string
    CreatedAt    time.Time
}

type Restriction struct {
    ID              int64
    ProfessionalID  int64
    Professional    string
    SourceName      string
    SourceSheet     string
    SourceRow       int
    CompetenceMonth int
    CompetenceYear  int
    PayerName       string
    PayerCPFRaw     string
    BeneficiaryName string
    BeneficiaryRaw  string
    PaymentDateRaw  string
    AmountRaw       string
    Reason          string
}

type App struct {
    db        *sql.DB
    dataRoot  string
    csrf      string
    previews  map[string]Preview
    previewMu sync.Mutex
}

func main() {
    var dataRoot, report string
    var selftest, seedReinstall, verifyReinstall bool
    args := os.Args[1:]
    for i := 0; i < len(args); i++ {
        switch args[i] {
        case "--data-root":
            if i+1 < len(args) { i++; dataRoot = args[i] }
        case "--report":
            if i+1 < len(args) { i++; report = args[i] }
        case "--selftest": selftest = true
        case "--seed-reinstall": seedReinstall = true
        case "--verify-reinstall": verifyReinstall = true
        }
    }
    if dataRoot == "" { dataRoot = defaultDataRoot() }

    if selftest {
        if err := runSelfTest(dataRoot, report); err != nil { writeReport(report, "MVP_ACCEPTANCE_FAIL\n"+err.Error()+"\n"); os.Exit(1) }
        return
    }
    if seedReinstall {
        if err := runSeedReinstall(dataRoot, report); err != nil { writeReport(report, "SEED_REINSTALL_FAIL\n"+err.Error()+"\n"); os.Exit(1) }
        return
    }
    if verifyReinstall {
        if err := runVerifyReinstall(dataRoot, report); err != nil { writeReport(report, "REINSTALL_PERSISTENCE_FAIL\n"+err.Error()+"\n"); os.Exit(1) }
        return
    }

    app, err := newApp(dataRoot)
    if err != nil { fatalDialog(err); return }
    defer app.db.Close()
    if err := app.serve(); err != nil { fatalDialog(err) }
}

func defaultDataRoot() string {
    if v := os.Getenv("GD_FISCAL_SAUDE_DATA_DIR"); v != "" { return v }
    if runtime.GOOS == "windows" {
        base := os.Getenv("LOCALAPPDATA")
        if base == "" { base = os.TempDir() }
        return filepath.Join(base, "GD Solucoes", "Fiscal Saude")
    }
    home, _ := os.UserHomeDir()
    return filepath.Join(home, ".gd-fiscal-saude")
}

func newApp(root string) (*App, error) {
    if err := os.MkdirAll(filepath.Join(root, "database"), 0755); err != nil { return nil, err }
    if err := os.MkdirAll(filepath.Join(root, "runtime"), 0755); err != nil { return nil, err }
    if err := os.MkdirAll(filepath.Join(root, "exports"), 0755); err != nil { return nil, err }
    dbPath := filepath.Join(root, "database", "fiscal_saude.sqlite3")
    db, err := sql.Open("sqlite", dbPath)
    if err != nil { return nil, err }
    db.SetMaxOpenConns(1)
    if err := initSchema(db); err != nil { db.Close(); return nil, err }
    return &App{db: db, dataRoot: root, csrf: randomToken(), previews: map[string]Preview{}}, nil
}

func initSchema(db *sql.DB) error {
    stmts := []string{
        `PRAGMA journal_mode=WAL;`,
        `PRAGMA foreign_keys=ON;`,
        `CREATE TABLE IF NOT EXISTS app_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
        `CREATE TABLE IF NOT EXISTS professionals (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            cpf TEXT NOT NULL UNIQUE,
            profession_key TEXT NOT NULL,
            registry TEXT NOT NULL DEFAULT '',
            registry_state TEXT NOT NULL DEFAULT '',
            active INTEGER NOT NULL DEFAULT 1,
            created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
        );`,
        `CREATE TABLE IF NOT EXISTS payments (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            professional_id INTEGER NOT NULL REFERENCES professionals(id),
            payer_name TEXT NOT NULL,
            payer_cpf TEXT NOT NULL,
            beneficiary_name TEXT NOT NULL,
            beneficiary_cpf TEXT NOT NULL,
            payment_date TEXT NOT NULL,
            amount_cents INTEGER NOT NULL,
            description TEXT NOT NULL DEFAULT 'ATENDIMENTO EM SAUDE',
            status TEXT NOT NULL DEFAULT 'PENDENTE_REVISAO',
            created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
        );`,
        `CREATE INDEX IF NOT EXISTS idx_payments_prof_date ON payments(professional_id,payment_date,status);`,
        `CREATE TABLE IF NOT EXISTS import_restrictions (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            professional_id INTEGER NOT NULL REFERENCES professionals(id),
            source_name TEXT NOT NULL DEFAULT '',
            source_sheet TEXT NOT NULL DEFAULT '',
            source_row INTEGER NOT NULL DEFAULT 0,
            competence_month INTEGER,
            competence_year INTEGER,
            payer_name TEXT NOT NULL DEFAULT '',
            payer_cpf_raw TEXT NOT NULL DEFAULT '',
            beneficiary_name TEXT NOT NULL DEFAULT '',
            beneficiary_cpf_raw TEXT NOT NULL DEFAULT '',
            payment_date_raw TEXT NOT NULL DEFAULT '',
            amount_raw TEXT NOT NULL DEFAULT '',
            issue_reason TEXT NOT NULL DEFAULT '',
            status TEXT NOT NULL DEFAULT 'PENDENTE',
            resolved_payment_id INTEGER REFERENCES payments(id),
            created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            resolved_at TEXT
        );`,
        `CREATE INDEX IF NOT EXISTS idx_restrictions_prof_status ON import_restrictions(professional_id,status);`,
    }
    for _, s := range stmts { if _, err := db.Exec(s); err != nil { return err } }
    _, err := db.Exec(`INSERT INTO app_meta(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(schemaVersion))
    return err
}

func randomToken() string {
    b := make([]byte, 24); _, _ = rand.Read(b); return hex.EncodeToString(b)
}

func onlyDigits(s string) string { return digitRE.ReplaceAllString(s, "") }

func cpfValid(cpf string) bool {
    cpf = onlyDigits(cpf)
    if len(cpf) != 11 { return false }
    same := true; for i := 1; i < 11; i++ { if cpf[i] != cpf[0] { same = false; break } }; if same { return false }
    nums := make([]int, 11)
    for i := range nums { nums[i] = int(cpf[i]-'0') }
    sum := 0; for i := 0; i < 9; i++ { sum += nums[i]*(10-i) }
    d := (sum*10)%11; if d == 10 { d = 0 }; if d != nums[9] { return false }
    sum = 0; for i := 0; i < 10; i++ { sum += nums[i]*(11-i) }
    d = (sum*10)%11; if d == 10 { d = 0 }
    return d == nums[10]
}

func normalizeCPF(raw string) (string, error) {
    d := onlyDigits(raw)
    if len(d) == 10 {
        c := "0"+d
        if cpfValid(c) { return c, nil }
    }
    if !cpfValid(d) { return "", errors.New("CPF inválido") }
    return d, nil
}

func normalizeText(s string) string {
    s = strings.ToLower(strings.TrimSpace(s))
    r := strings.NewReplacer("á","a","à","a","â","a","ã","a","ä","a","é","e","ê","e","ë","e","í","i","ï","i","ó","o","ô","o","õ","o","ö","o","ú","u","ü","u","ç","c")
    return r.Replace(s)
}

func monthFromText(s string) int {
    n := normalizeText(s)
    for name, m := range monthNames { if strings.Contains(n, name) { return m } }
    return 0
}

func yearFromText(s string) int {
    m := yearRE.FindStringSubmatch(s); if len(m) == 2 { y,_ := strconv.Atoi(m[1]); return y }; return 0
}

func parseMoney(raw string) (int64, error) {
    s := strings.TrimSpace(raw)
    s = strings.ReplaceAll(s, "R$", "")
    s = strings.ReplaceAll(s, " ", "")
    if s == "" { return 0, errors.New("valor vazio") }
    if strings.Contains(s, ",") { s = strings.ReplaceAll(s, ".", ""); s = strings.ReplaceAll(s, ",", ".") }
    v, err := strconv.ParseFloat(s, 64); if err != nil || v <= 0 { return 0, errors.New("valor inválido") }
    return int64(v*100 + 0.5), nil
}

func formatMoney(c int64) string { return fmt.Sprintf("R$ %.2f", float64(c)/100) }
func csvMoney(c int64) string { return strings.Replace(fmt.Sprintf("%.2f", float64(c)/100), ".", ",", 1) }

func parseDate(raw string, month, year int) (string, error) {
    s := strings.TrimSpace(raw); if s == "" { return "", errors.New("data vazia") }
    if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 31 && month >= 1 && month <= 12 && year >= 2000 {
        t := time.Date(year, time.Month(month), n, 0,0,0,0,time.Local)
        if t.Day() != n || int(t.Month()) != month { return "", errors.New("data inválida") }
        return t.Format("2006-01-02"), nil
    }
    if f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64); err == nil && f > 25000 && f < 100000 {
        if t, err := excelize.ExcelDateToTime(f, false); err == nil { return t.Format("2006-01-02"), nil }
    }
    layouts := []string{"2006-01-02","02/01/2006","2/1/2006","02-01-2006","2-1-2006","01/02/2006","1/2/2006","2006-01-02 15:04:05","02/01/06"}
    for _, l := range layouts { if t, err := time.Parse(l,s); err == nil { return t.Format("2006-01-02"), nil } }
    return "", errors.New("data inválida")
}

func splitParty(raw string) (string,string) {
    d := onlyDigits(raw)
    name := strings.TrimSpace(raw)
    if len(d) >= 11 {
        // Remove common CPF presentations from display name.
        candidates := []string{d, fmt.Sprintf("%s.%s.%s-%s",d[0:3],d[3:6],d[6:9],d[9:11])}
        for _, c := range candidates { name = strings.ReplaceAll(name,c,"") }
        name = strings.Trim(name," -–—;,:/")
    }
    return name,d
}

type block struct { headerRow,start,end,nameCol,payerCol,beneficiaryCol,dateCol,amountCol,month,year int }

func getCell(row []string, col int) string { if col < 0 || col >= len(row) { return "" }; return strings.TrimSpace(row[col]) }

func maxColumns(rows [][]string) int { m:=0; for _,r:=range rows { if len(r)>m {m=len(r)} }; return m }

func findPayerCols(row []string) []int {
    var out []int
    for i,v := range row { n:=normalizeText(v); if strings.Contains(n,"cpf") && (strings.Contains(n,"responsavel") || strings.Contains(n,"pagador")) { out=append(out,i) } }
    return out
}

func isNameHeader(v string) bool { n:=normalizeText(v); return n=="nome" || (strings.Contains(n,"nome") && (strings.Contains(n,"responsavel")||strings.Contains(n,"pagador"))) }
func findNameCol(row []string,payer,max int) int {
    for i:=payer-1; i>=0 && i>=payer-10; i-- { if isNameHeader(getCell(row,i)) {return i} }
    for i:=payer+1; i<max && i<=payer+10; i++ { if isNameHeader(getCell(row,i)) {return i} }
    return -1
}
func findColumn(row []string,start,end int, required string, forbidden string) int {
    if start<0 {start=0}; if end>len(row){end=len(row)}
    for i:=start;i<end;i++ { n:=normalizeText(row[i]); if strings.Contains(n,required) && (forbidden=="" || !strings.Contains(n,forbidden)) { return i } }
    return -1
}
func findBeneficiary(row []string,start,end int) int {
    if start<0{start=0}; if end>len(row){end=len(row)}
    for i:=start;i<end;i++ { n:=normalizeText(row[i]); if strings.Contains(n,"paciente")||strings.Contains(n,"beneficiario") {return i} }
    return -1
}

func globalYear(rows [][]string, title string) int {
    if y:=yearFromText(title); y>0{return y}
    for i:=0;i<len(rows)&&i<20;i++ { for _,v:=range rows[i] { if y:=yearFromText(v); y>0{return y} } }
    return 0
}

func blockContext(rows [][]string, header,start,end int,title string, gy int)(int,int){
    for r:=header-1; r>=0 && r>=header-5; r-- { for c:=start;c<end;c++ { v:=getCell(rows[r],c); if m:=monthFromText(v);m>0 { y:=yearFromText(v);if y==0{y=gy};return m,y } } }
    if m:=monthFromText(title);m>0 { y:=yearFromText(title);if y==0{y=gy};return m,y }
    return 0,gy
}

func detectBlocks(rows [][]string,title string)[]block{
    var out []block; max:=maxColumns(rows); gy:=globalYear(rows,title)
    for ri,row:=range rows {
        payers:=findPayerCols(row); if len(payers)==0{continue}
        names:=make([]int,len(payers));for i,p:=range payers{names[i]=findNameCol(row,p,max)}
        for pos,p:=range payers{
            start:=0;end:=max
            if len(payers)>1{
                if names[pos]>=0 {start=names[pos]} else {start=p-10;if start<0{start=0}}
                if pos+1<len(payers){ if names[pos+1]>p {end=names[pos+1]} else {end=payers[pos+1]-10;if end<=p{end=payers[pos+1]}} }
            }
            bcol:=findBeneficiary(row,start,end); dcol:=findColumn(row,start,end,"data","nascimento"); acol:=findColumn(row,start,end,"valor","")
            if dcol<0||acol<0{continue}
            m,y:=blockContext(rows,ri,start,end,title,gy)
            out=append(out,block{ri,start,end,names[pos],p,bcol,dcol,acol,m,y})
        }
    }
    return out
}

func isTotalRow(row []string,b block)bool{ for c:=b.start;c<b.end;c++{if strings.Contains(normalizeText(getCell(row,c)),"valor total mes"){return true}};return false }
func isRepeatedHeader(row []string,b block)bool{ n:=normalizeText(getCell(row,b.payerCol));return strings.Contains(n,"cpf")&&(strings.Contains(n,"responsavel")||strings.Contains(n,"pagador")) }

func parseWorkbook(data []byte) (Preview,error){
    f,err:=excelize.OpenReader(bytes.NewReader(data));if err!=nil{return Preview{},fmt.Errorf("arquivo XLSX inválido: %w",err)};defer f.Close()
    p:=Preview{CreatedAt:time.Now()}; comp:=map[string]bool{}; recognized:=false
    for _,sheet:=range f.GetSheetList(){
        if vis,e:=f.GetSheetVisible(sheet);e==nil&&!vis{continue}
        rows,err:=f.GetRows(sheet);if err!=nil{return p,err}; blocks:=detectBlocks(rows,sheet);if len(blocks)==0{continue};recognized=true
        for _,b:=range blocks{
            for ri:=b.headerRow+1;ri<len(rows);ri++{
                row:=rows[ri];if isTotalRow(row,b)||isRepeatedHeader(row,b){break}
                any:=false;for c:=b.start;c<b.end;c++{if getCell(row,c)!=""{any=true;break}};if !any{continue}
                payerName:=getCell(row,b.nameCol);payerRaw:=getCell(row,b.payerCol);benefRaw:=getCell(row,b.beneficiaryCol);dateRaw:=getCell(row,b.dateCol);amountRaw:=getCell(row,b.amountCol)
                if payerName==""{n,_:=splitParty(payerRaw);payerName=n}
                common:=IssueRow{SourceSheet:sheet,SourceRow:ri+1,CompetenceMonth:b.month,CompetenceYear:b.year,PayerName:payerName,PayerCPFRaw:onlyDigits(payerRaw),PaymentDateRaw:dateRaw,AmountRaw:amountRaw}
                if dateRaw==""&&amountRaw==""{common.Reason="Sem atendimento no período";p.Skipped=append(p.Skipped,common);continue}
                payerCPF,eCPF:=normalizeCPF(payerRaw)
                benName,benDigits:=splitParty(benefRaw);if strings.TrimSpace(benefRaw)==""{benName=payerName;benDigits=payerCPF}; if benName==""{benName=payerName}
                benCPF,eBen:=normalizeCPF(benDigits);dt,eDate:=parseDate(dateRaw,b.month,b.year);amount,eAmount:=parseMoney(amountRaw)
                reasons:=[]string{};if payerName==""{reasons=append(reasons,"nome do responsável ausente")};if eCPF!=nil{reasons=append(reasons,"CPF do responsável inválido")};if eBen!=nil{reasons=append(reasons,"CPF do beneficiário inválido")};if eDate!=nil{reasons=append(reasons,"data inválida")};if eAmount!=nil{reasons=append(reasons,"valor inválido")}
                if len(reasons)>0{common.BeneficiaryName=benName;common.BeneficiaryRaw=benDigits;common.Reason=strings.Join(reasons,"; ");p.Restricted=append(p.Restricted,common);continue}
                cm,cy:=b.month,b.year;if t,e:=time.Parse("2006-01-02",dt);e==nil{if cm==0{cm=int(t.Month())};if cy==0{cy=t.Year()}}
                p.Valid=append(p.Valid,ImportRow{sheet,ri+1,cm,cy,payerName,payerCPF,benName,benCPF,dt,amount})
                if cm>0&&cy>0{comp[fmt.Sprintf("%02d/%04d",cm,cy)]=true}
            }
        }
    }
    if !recognized{p.Structural=append(p.Structural,IssueRow{Reason:"Cabeçalho não reconhecido. A planilha precisa conter Nome/CPF responsável, Data e Valor."})}
    for k:=range comp{p.Competences=append(p.Competences,k)};sort.Strings(p.Competences)
    return p,nil
}

func (a *App) addProfessional(name,cpf,key,registry,state string)(int64,error){
    name=strings.TrimSpace(name);if name==""{return 0,errors.New("nome do profissional é obrigatório")};ncpf,err:=normalizeCPF(cpf);if err!=nil{return 0,err};if _,ok:=occupationCodes[key];!ok{return 0,errors.New("profissão inválida")}
    r,err:=a.db.Exec(`INSERT INTO professionals(name,cpf,profession_key,registry,registry_state,active) VALUES(?,?,?,?,?,1)`,name,ncpf,key,strings.TrimSpace(registry),strings.TrimSpace(state));if err!=nil{if strings.Contains(strings.ToLower(err.Error()),"unique"){return 0,errors.New("CPF profissional já cadastrado")};return 0,err};return r.LastInsertId()
}

func (a *App) professionals()([]Professional,error){
    rows,err:=a.db.Query(`SELECT id,name,cpf,profession_key,registry,registry_state,active FROM professionals ORDER BY active DESC,name`);if err!=nil{return nil,err};defer rows.Close();var out []Professional
    for rows.Next(){var p Professional;var act int;if err:=rows.Scan(&p.ID,&p.Name,&p.CPF,&p.ProfessionKey,&p.Registry,&p.RegistryState,&act);err!=nil{return nil,err};p.Active=act==1;out=append(out,p)};return out,rows.Err()
}

func (a *App) professional(id int64)(Professional,error){var p Professional;var act int;err:=a.db.QueryRow(`SELECT id,name,cpf,profession_key,registry,registry_state,active FROM professionals WHERE id=?`,id).Scan(&p.ID,&p.Name,&p.CPF,&p.ProfessionKey,&p.Registry,&p.RegistryState,&act);p.Active=act==1;return p,err}

func importPreviewTx(tx *sql.Tx,p Preview,professionalID int64)(created,restrictions,duplicates int,error error){
    var exists int;if e:=tx.QueryRow(`SELECT COUNT(*) FROM professionals WHERE id=? AND active=1`,professionalID).Scan(&exists);e!=nil||exists!=1{return 0,0,0,errors.New("profissional não encontrado ou inativo")}
    for _,r:=range p.Valid{
        var n int;e:=tx.QueryRow(`SELECT COUNT(*) FROM payments WHERE professional_id=? AND payer_cpf=? AND beneficiary_cpf=? AND payment_date=? AND amount_cents=? AND status!='CANCELADO'`,professionalID,r.PayerCPF,r.BeneficiaryCPF,r.PaymentDate,r.AmountCents).Scan(&n);if e!=nil{return created,restrictions,duplicates,e};if n>0{duplicates++;continue}
        _,e=tx.Exec(`INSERT INTO payments(professional_id,payer_name,payer_cpf,beneficiary_name,beneficiary_cpf,payment_date,amount_cents,description,status) VALUES(?,?,?,?,?,?,?,'ATENDIMENTO EM SAUDE','PENDENTE_REVISAO')`,professionalID,r.PayerName,r.PayerCPF,r.BeneficiaryName,r.BeneficiaryCPF,r.PaymentDate,r.AmountCents);if e!=nil{return created,restrictions,duplicates,e};created++
    }
    for _,r:=range p.Restricted{
        _,e:=tx.Exec(`INSERT INTO import_restrictions(professional_id,source_name,source_sheet,source_row,competence_month,competence_year,payer_name,payer_cpf_raw,beneficiary_name,beneficiary_cpf_raw,payment_date_raw,amount_raw,issue_reason,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'PENDENTE')`,professionalID,p.SourceName,r.SourceSheet,r.SourceRow,nullInt(r.CompetenceMonth),nullInt(r.CompetenceYear),r.PayerName,r.PayerCPFRaw,r.BeneficiaryName,r.BeneficiaryRaw,r.PaymentDateRaw,r.AmountRaw,r.Reason);if e!=nil{return created,restrictions,duplicates,e};restrictions++
    }
    return
}
func nullInt(v int)any{if v==0{return nil};return v}

func (a *App) importPreview(p Preview,professionalID int64)(int,int,int,error){tx,err:=a.db.Begin();if err!=nil{return 0,0,0,err};c,r,d,e:=importPreviewTx(tx,p,professionalID);if e!=nil{tx.Rollback();return 0,0,0,e};if e=tx.Commit();e!=nil{return 0,0,0,e};return c,r,d,nil}

func (a *App) listPayments(prof int64) ([]Payment,error){
    q:=`SELECT p.id,p.professional_id,pr.name,p.payer_name,p.payer_cpf,p.beneficiary_name,p.beneficiary_cpf,p.payment_date,p.amount_cents,p.status FROM payments p JOIN professionals pr ON pr.id=p.professional_id`;args:=[]any{};if prof>0{q+=` WHERE p.professional_id=?`;args=append(args,prof)};q+=` ORDER BY p.payment_date DESC,p.id DESC`
    rows,err:=a.db.Query(q,args...);if err!=nil{return nil,err};defer rows.Close();var out []Payment;for rows.Next(){var p Payment;if err:=rows.Scan(&p.ID,&p.ProfessionalID,&p.Professional,&p.PayerName,&p.PayerCPF,&p.BeneficiaryName,&p.BeneficiaryCPF,&p.PaymentDate,&p.AmountCents,&p.Status);err!=nil{return nil,err};out=append(out,p)};return out,rows.Err()
}

func (a *App) validatePayment(id int64)error{_,err:=a.db.Exec(`UPDATE payments SET status='VALIDADO_RECEITA_SAUDE' WHERE id=? AND status='PENDENTE_REVISAO'`,id);return err}
func (a *App) validateAll(prof int64,month,year int)error{prefix:=fmt.Sprintf("%04d-%02d-",year,month);_,err:=a.db.Exec(`UPDATE payments SET status='VALIDADO_RECEITA_SAUDE' WHERE professional_id=? AND payment_date LIKE ? AND status='PENDENTE_REVISAO'`,prof,prefix+"%");return err}

func csvRow(p Professional,r Payment)([]string,error){
    t,err:=time.Parse("2006-01-02",r.PaymentDate);if err!=nil{return nil,err};occ:=occupationCodes[p.ProfessionKey];if occ==""{return nil,errors.New("ocupação profissional inválida")}
    if !cpfValid(r.PayerCPF)||!cpfValid(r.BeneficiaryCPF)||!cpfValid(p.CPF){return nil,errors.New("CPF inválido impede exportação")}
    desc:=strings.ReplaceAll(r.PayerName,";",",");if strings.TrimSpace(desc)==""{desc="ATENDIMENTO EM SAUDE"}
    return []string{t.Format("02/01/2006"),incomeCode,occ,csvMoney(r.AmountCents),"","ATENDIMENTO EM SAUDE","PF",r.PayerCPF,r.BeneficiaryCPF,"","","","","S",p.CPF,p.Registry},nil
}

func (a *App) exportCSV(profID int64,month,year int)([]byte,int,int64,error){
    p,err:=a.professional(profID);if err!=nil{return nil,0,0,err};prefix:=fmt.Sprintf("%04d-%02d-",year,month)
    rows,err:=a.db.Query(`SELECT p.id,p.professional_id,pr.name,p.payer_name,p.payer_cpf,p.beneficiary_name,p.beneficiary_cpf,p.payment_date,p.amount_cents,p.status FROM payments p JOIN professionals pr ON pr.id=p.professional_id WHERE p.professional_id=? AND p.payment_date LIKE ? AND p.status='VALIDADO_RECEITA_SAUDE' ORDER BY p.payment_date,p.id`,profID,prefix+"%");if err!=nil{return nil,0,0,err};defer rows.Close()
    var buf bytes.Buffer;w:=csv.NewWriter(&buf);w.Comma=';';w.UseCRLF=true;var ids []int64;count:=0;var total int64
    for rows.Next(){var r Payment;if err:=rows.Scan(&r.ID,&r.ProfessionalID,&r.Professional,&r.PayerName,&r.PayerCPF,&r.BeneficiaryName,&r.BeneficiaryCPF,&r.PaymentDate,&r.AmountCents,&r.Status);err!=nil{return nil,0,0,err};line,err:=csvRow(p,r);if err!=nil{return nil,0,0,err};if err:=w.Write(line);err!=nil{return nil,0,0,err};ids=append(ids,r.ID);count++;total+=r.AmountCents};if err:=rows.Err();err!=nil{return nil,0,0,err};w.Flush();if err:=w.Error();err!=nil{return nil,0,0,err};if count==0{return nil,0,0,errors.New("nenhum lançamento validado para a competência e profissional selecionados")}
    tx,err:=a.db.Begin();if err!=nil{return nil,0,0,err};for _,id:=range ids{if _,err:=tx.Exec(`UPDATE payments SET status='EXPORTADO_CSV' WHERE id=?`,id);err!=nil{tx.Rollback();return nil,0,0,err}};if err:=tx.Commit();err!=nil{return nil,0,0,err}
    return buf.Bytes(),count,total,nil
}

func (a *App) restrictions()([]Restriction,error){rows,err:=a.db.Query(`SELECT r.id,r.professional_id,p.name,r.source_name,r.source_sheet,r.source_row,COALESCE(r.competence_month,0),COALESCE(r.competence_year,0),r.payer_name,r.payer_cpf_raw,r.beneficiary_name,r.beneficiary_cpf_raw,r.payment_date_raw,r.amount_raw,r.issue_reason FROM import_restrictions r JOIN professionals p ON p.id=r.professional_id WHERE r.status='PENDENTE' ORDER BY r.id DESC`);if err!=nil{return nil,err};defer rows.Close();var out []Restriction;for rows.Next(){var r Restriction;if err:=rows.Scan(&r.ID,&r.ProfessionalID,&r.Professional,&r.SourceName,&r.SourceSheet,&r.SourceRow,&r.CompetenceMonth,&r.CompetenceYear,&r.PayerName,&r.PayerCPFRaw,&r.BeneficiaryName,&r.BeneficiaryRaw,&r.PaymentDateRaw,&r.AmountRaw,&r.Reason);err!=nil{return nil,err};out=append(out,r)};return out,rows.Err()}

func (a *App) resolveRestriction(id int64,payerName,payerCPF,benName,benCPF,dateRaw,amountRaw string,same bool)error{
    tx,err:=a.db.Begin();if err!=nil{return err};defer func(){if err!=nil{_ = tx.Rollback()}}()
    var prof int64;var status string;err=tx.QueryRow(`SELECT professional_id,status FROM import_restrictions WHERE id=?`,id).Scan(&prof,&status);if err!=nil{return err};if status!="PENDENTE"{return errors.New("pendência já resolvida")}
    pc,err:=normalizeCPF(payerCPF);if err!=nil{return err};if strings.TrimSpace(payerName)==""{return errors.New("nome do pagador obrigatório")};if same{benName=payerName;benCPF=pc};bc,err:=normalizeCPF(benCPF);if err!=nil{return err};if strings.TrimSpace(benName)==""{return errors.New("nome do beneficiário obrigatório")}
    dt,err:=parseDate(dateRaw,0,0);if err!=nil{return err};amt,err:=parseMoney(amountRaw);if err!=nil{return err}
    r,err:=tx.Exec(`INSERT INTO payments(professional_id,payer_name,payer_cpf,beneficiary_name,beneficiary_cpf,payment_date,amount_cents,description,status) VALUES(?,?,?,?,?,?,?,'ATENDIMENTO EM SAUDE','PENDENTE_REVISAO')`,prof,payerName,pc,benName,bc,dt,amt);if err!=nil{return err};pid,_:=r.LastInsertId();_,err=tx.Exec(`UPDATE import_restrictions SET status='RESOLVIDO',resolved_payment_id=?,resolved_at=CURRENT_TIMESTAMP WHERE id=?`,pid,id);if err!=nil{return err};err=tx.Commit();return err
}

func (a *App) serve() error {
    mux:=http.NewServeMux();mux.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","text/plain");io.WriteString(w,"ok")});mux.HandleFunc("/",a.home);mux.HandleFunc("/professionals",a.professionalsPage);mux.HandleFunc("/import",a.importPage);mux.HandleFunc("/payments",a.paymentsPage);mux.HandleFunc("/payments/validate",a.validatePage);mux.HandleFunc("/payments/validate-all",a.validateAllPage);mux.HandleFunc("/restrictions",a.restrictionsPage);mux.HandleFunc("/restrictions/resolve",a.resolveRestrictionPage);mux.HandleFunc("/export",a.exportPage)
    ln,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{return err};defer ln.Close();port:=ln.Addr().(*net.TCPAddr).Port;portFile:=filepath.Join(a.dataRoot,"runtime","port.txt");_ = os.WriteFile(portFile,[]byte(strconv.Itoa(port)),0644)
    url:=fmt.Sprintf("http://127.0.0.1:%d/",port);if os.Getenv("GD_FISCAL_SAUDE_TEST_MODE")!="1"{go openBrowser(url)}
    srv:=&http.Server{Handler:mux};return srv.Serve(ln)
}

func openBrowser(url string){if runtime.GOOS=="windows"{_ = exec.Command("rundll32","url.dll,FileProtocolHandler",url).Start()}else{_ = exec.Command("xdg-open",url).Start()}}
func fatalDialog(err error){if runtime.GOOS=="windows"{_ = exec.Command("powershell","-NoProfile","-Command",fmt.Sprintf("Add-Type -AssemblyName PresentationFramework; [System.Windows.MessageBox]::Show('%s','GD Fiscal Saúde')",strings.ReplaceAll(err.Error(),"'","''"))).Run()}else{log.Println(err)}}

func csrfOK(r *http.Request,token string)bool{return r.FormValue("csrf")==token}
func parseID(s string)int64{v,_:=strconv.ParseInt(s,10,64);return v}
func parseInt(s string)int{v,_:=strconv.Atoi(s);return v}

func (a *App) render(w http.ResponseWriter,title,body string,data any){
    page:=baseHTML; t,err:=template.New("page").Funcs(template.FuncMap{"money":formatMoney,"monthLabel":func(i int)string{if i>=1&&i<=12{return monthLabels[i]};return ""},"profession":func(k string)string{return professionLabels[k]}}).Parse(page);if err!=nil{http.Error(w,err.Error(),500);return};payload:=struct{Title,Body,CSRF string;Data any}{title,body,a.csrf,data};if err:=t.Execute(w,payload);err!=nil{log.Println(err)}
}

func (a *App) home(w http.ResponseWriter,r *http.Request){if r.URL.Path!="/"{http.NotFound(w,r);return};ps,_:=a.professionals();pays,_:=a.listPayments(0);rs,_:=a.restrictions();a.render(w,"Início",homeHTML,map[string]any{"Professionals":len(ps),"Payments":len(pays),"Restrictions":len(rs)})}

func (a *App) professionalsPage(w http.ResponseWriter,r *http.Request){msg:="";if r.Method==http.MethodPost{if err:=r.ParseForm();err!=nil||!csrfOK(r,a.csrf){http.Error(w,"requisição inválida",400);return};_,err:=a.addProfessional(r.FormValue("name"),r.FormValue("cpf"),r.FormValue("profession_key"),r.FormValue("registry"),r.FormValue("registry_state"));if err!=nil{msg=err.Error()}else{msg="Profissional cadastrado com sucesso."}};ps,_:=a.professionals();a.render(w,"Profissionais",professionalsHTML,map[string]any{"List":ps,"Msg":msg})}

func (a *App) importPage(w http.ResponseWriter,r *http.Request){
    ps,_:=a.professionals();data:=map[string]any{"Professionals":ps,"Msg":"","Preview":Preview{}}
    if r.Method==http.MethodPost{
        if err:=r.ParseMultipartForm(32<<20);err!=nil{data["Msg"]="Falha ao ler formulário: "+err.Error();a.render(w,"Importar Excel",importHTML,data);return};if !csrfOK(r,a.csrf){http.Error(w,"requisição inválida",400);return};action:=r.FormValue("action")
        if action=="preview"{file,header,err:=r.FormFile("spreadsheet");if err!=nil{data["Msg"]="Selecione um arquivo .xlsx."}else{defer file.Close();raw,_:=io.ReadAll(io.LimitReader(file,50<<20));p,err:=parseWorkbook(raw);if err!=nil{data["Msg"]=err.Error()}else{p.Token=randomToken();p.SourceName=header.Filename;a.previewMu.Lock();a.previews[p.Token]=p;a.previewMu.Unlock();data["Preview"]=p}}}
        if action=="confirm"{token:=r.FormValue("preview_token");prof:=parseID(r.FormValue("professional_id"));a.previewMu.Lock();p,ok:=a.previews[token];a.previewMu.Unlock();if !ok{data["Msg"]="Prévia expirada. Gere novamente."}else if prof==0{data["Msg"]="Selecione o profissional.";data["Preview"]=p}else{c,rr,d,err:=a.importPreview(p,prof);if err!=nil{data["Msg"]=err.Error();data["Preview"]=p}else{data["Msg"]=fmt.Sprintf("Importação concluída: %d lançamentos; %d pendências; %d sem atendimento; %d duplicados ignorados.",c,rr,len(p.Skipped),d);a.previewMu.Lock();delete(a.previews,token);a.previewMu.Unlock()}}}
    }
    a.render(w,"Importar Excel",importHTML,data)
}

func (a *App) paymentsPage(w http.ResponseWriter,r *http.Request){prof:=parseID(r.URL.Query().Get("professional_id"));ps,_:=a.professionals();rows,_:=a.listPayments(prof);a.render(w,"Lançamentos",paymentsHTML,map[string]any{"Professionals":ps,"Rows":rows,"Selected":prof})}
func (a *App) validatePage(w http.ResponseWriter,r *http.Request){if r.Method!=http.MethodPost{http.Redirect(w,r,"/payments",303);return};_ = r.ParseForm();if !csrfOK(r,a.csrf){http.Error(w,"requisição inválida",400);return};_ = a.validatePayment(parseID(r.FormValue("id")));http.Redirect(w,r,"/payments?professional_id="+r.FormValue("professional_id"),303)}
func (a *App) validateAllPage(w http.ResponseWriter,r *http.Request){if r.Method!=http.MethodPost{http.Redirect(w,r,"/payments",303);return};_ = r.ParseForm();if !csrfOK(r,a.csrf){http.Error(w,"requisição inválida",400);return};prof:=parseID(r.FormValue("professional_id"));_ = a.validateAll(prof,parseInt(r.FormValue("month")),parseInt(r.FormValue("year")));http.Redirect(w,r,"/payments?professional_id="+r.FormValue("professional_id"),303)}

func (a *App) restrictionsPage(w http.ResponseWriter,r *http.Request){rs,_:=a.restrictions();a.render(w,"Pendências",restrictionsHTML,map[string]any{"Rows":rs})}
func (a *App) resolveRestrictionPage(w http.ResponseWriter,r *http.Request){if r.Method!=http.MethodPost{http.Redirect(w,r,"/restrictions",303);return};_ = r.ParseForm();if !csrfOK(r,a.csrf){http.Error(w,"requisição inválida",400);return};err:=a.resolveRestriction(parseID(r.FormValue("id")),r.FormValue("payer_name"),r.FormValue("payer_cpf"),r.FormValue("beneficiary_name"),r.FormValue("beneficiary_cpf"),r.FormValue("payment_date"),r.FormValue("amount"),r.FormValue("same_person")=="on");if err!=nil{http.Error(w,err.Error(),400);return};http.Redirect(w,r,"/restrictions",303)}

func (a *App) exportPage(w http.ResponseWriter,r *http.Request){ps,_:=a.professionals();msg:="";if r.Method==http.MethodPost{_ = r.ParseForm();if !csrfOK(r,a.csrf){http.Error(w,"requisição inválida",400);return};prof:=parseID(r.FormValue("professional_id"));m:=parseInt(r.FormValue("month"));y:=parseInt(r.FormValue("year"));content,count,total,err:=a.exportCSV(prof,m,y);if err!=nil{msg=err.Error()}else{w.Header().Set("Content-Type","text/csv; charset=utf-8");w.Header().Set("Content-Disposition",fmt.Sprintf(`attachment; filename="escrituracao_%04d_%02d.csv"`,y,m));w.Header().Set("X-GD-Rows",strconv.Itoa(count));w.Header().Set("X-GD-Total-Cents",strconv.FormatInt(total,10));_,_=w.Write(content);return}};a.render(w,"Exportar escrituração",exportHTML,map[string]any{"Professionals":ps,"Msg":msg})}

const baseHTML = `<!doctype html><html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} — GD Fiscal Saúde</title><style>
:root{--g:#0D4E3A;--gold:#B88B30;--bg:#f5f7f6;--ink:#18201d;--mut:#66736e;--danger:#9e2a2b;--warn:#7a5a00}*{box-sizing:border-box}body{margin:0;background:var(--bg);font:15px/1.45 Segoe UI,Arial,sans-serif;color:var(--ink)}header{background:white;border-bottom:3px solid var(--g);padding:18px 28px;display:flex;align-items:center;gap:28px;position:sticky;top:0;z-index:2}.brand{font-weight:800;color:var(--g);font-size:20px}.brand span{color:var(--gold)}nav a{color:var(--g);text-decoration:none;font-weight:650;margin-right:18px}main{max-width:1180px;margin:28px auto;padding:0 22px}.panel{background:white;border:1px solid #dfe6e2;border-radius:12px;padding:20px;margin:0 0 18px;box-shadow:0 2px 8px #00000008}h1{color:var(--g);margin:0 0 18px}h2{color:var(--g);font-size:18px}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:14px}.metric{padding:18px;border-left:4px solid var(--gold);background:white;border-radius:8px}.metric b{font-size:26px;color:var(--g);display:block}label{display:block;font-weight:650;margin:10px 0 5px}input,select{width:100%;padding:10px;border:1px solid #bcc9c3;border-radius:7px;background:white}.row{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.btn,button{display:inline-block;border:0;border-radius:7px;padding:10px 14px;background:var(--g);color:white;text-decoration:none;font-weight:700;cursor:pointer}.btn.secondary{background:#e8efec;color:var(--g)}table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:9px;border-bottom:1px solid #e5eae8;vertical-align:top}th{color:var(--g);font-size:13px}.muted{color:var(--mut)}.ok{padding:12px;background:#edf8f2;border-left:4px solid var(--g);margin-bottom:15px}.warn{padding:12px;background:#fff8e1;border-left:4px solid var(--gold);margin:12px 0}.bad{color:var(--danger);font-weight:700}.tag{padding:3px 7px;border-radius:12px;background:#edf3f0;font-size:12px;white-space:nowrap}@media(max-width:720px){.row{grid-template-columns:1fr}header{display:block}nav{margin-top:10px}table{font-size:12px}}
</style></head><body><header><div class="brand">GD <span>Fiscal Saúde</span></div><nav><a href="/">Início</a><a href="/professionals">Profissionais</a><a href="/import">Importar Excel</a><a href="/payments">Lançamentos</a><a href="/restrictions">Pendências</a><a href="/export">Exportar</a></nav></header><main><h1>{{.Title}}</h1>{{template "body" .}}</main></body></html>`

const homeHTML = `{{define "body"}}{{$d:=.Data}}<div class="grid"><div class="metric"><b>{{index $d "Professionals"}}</b>profissionais cadastrados</div><div class="metric"><b>{{index $d "Payments"}}</b>lançamentos salvos</div><div class="metric"><b>{{index $d "Restrictions"}}</b>pendências para corrigir</div></div><section class="panel"><h2>Fluxo operacional</h2><p>Cadastre o profissional, importe a planilha Excel, confira a prévia, valide os lançamentos e exporte a escrituração.</p></section>{{end}}`

const professionalsHTML = `{{define "body"}}{{$d:=.Data}}{{with index $d "Msg"}}{{if .}}<div class="ok">{{.}}</div>{{end}}{{end}}<section class="panel"><h2>Novo profissional</h2><form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><div class="row"><div><label>Nome</label><input name="name" required></div><div><label>CPF</label><input name="cpf" required></div><div><label>Profissão</label><select name="profession_key"><option value="PSICOLOGO">Psicólogo</option><option value="MEDICO">Médico</option><option value="ODONTOLOGO_DENTISTA">Odontólogo/Dentista</option><option value="FONOAUDIOLOGO">Fonoaudiólogo</option><option value="FISIOTERAPEUTA">Fisioterapeuta</option><option value="TERAPEUTA_OCUPACIONAL">Terapeuta Ocupacional</option></select></div><div><label>Registro profissional</label><input name="registry"></div><div><label>UF do registro</label><input name="registry_state" value="RS" maxlength="2"></div></div><p><button>Cadastrar profissional</button></p></form></section><section class="panel"><h2>Profissionais</h2><table><tr><th>Nome</th><th>CPF</th><th>Profissão</th><th>Registro</th></tr>{{range index $d "List"}}<tr><td>{{.Name}}</td><td>{{.CPF}}</td><td>{{profession .ProfessionKey}}</td><td>{{.Registry}} {{.RegistryState}}</td></tr>{{else}}<tr><td colspan="4" class="muted">Nenhum profissional cadastrado.</td></tr>{{end}}</table></section>{{end}}`

const importHTML = `{{define "body"}}{{$d:=.Data}}{{with index $d "Msg"}}{{if .}}<div class="ok">{{.}}</div>{{end}}{{end}}<section class="panel"><h2>1. Selecionar planilha</h2><form method="post" enctype="multipart/form-data"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="preview"><label>Arquivo Excel (.xlsx)</label><input type="file" name="spreadsheet" accept=".xlsx" required><p><button>Gerar prévia</button></p></form></section>{{$p:=index $d "Preview"}}{{if $p.Token}}<section class="panel"><h2>2. Prévia da importação</h2><p><b>{{$p.SourceName}}</b></p><div class="grid"><div class="metric"><b>{{len $p.Valid}}</b>importáveis</div><div class="metric"><b>{{len $p.Skipped}}</b>sem atendimento</div><div class="metric"><b>{{len $p.Restricted}}</b>pendências</div><div class="metric"><b>{{len $p.Structural}}</b>erros estruturais</div></div><p class="muted">Competências: {{range $p.Competences}}{{.}} &nbsp;{{end}}</p>{{if $p.Restricted}}<div class="warn"><b>Pendências:</b> têm movimento informado, mas dados inválidos. Serão salvas para correção e não entrarão no CSV.</div>{{end}}<table><tr><th>Linha</th><th>Competência</th><th>Pagador</th><th>CPF</th><th>Data</th><th>Valor</th></tr>{{range $p.Valid}}<tr><td>{{.SourceSheet}}!{{.SourceRow}}</td><td>{{printf "%02d/%04d" .CompetenceMonth .CompetenceYear}}</td><td>{{.PayerName}}</td><td>{{.PayerCPF}}</td><td>{{.PaymentDate}}</td><td>{{money .AmountCents}}</td></tr>{{end}}</table></section><section class="panel"><h2>3. Confirmar para o profissional</h2><form method="post" enctype="multipart/form-data"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="confirm"><input type="hidden" name="preview_token" value="{{$p.Token}}"><label>Profissional</label><select name="professional_id" required><option value="">Selecione</option>{{range index $d "Professionals"}}<option value="{{.ID}}">{{.Name}} — {{.CPF}}</option>{{end}}</select><p><button>Importar e salvar</button></p></form></section>{{end}}{{end}}`

const paymentsHTML = `{{define "body"}}{{$d:=.Data}}<section class="panel"><form method="get"><label>Filtrar por profissional</label><select name="professional_id" onchange="this.form.submit()"><option value="">Todos</option>{{range index $d "Professionals"}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></form></section>{{if index $d "Selected"}}<section class="panel"><h2>Validação em lote</h2><form method="post" action="/payments/validate-all"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="professional_id" value="{{index $d "Selected"}}"><div class="row"><div><label>Mês</label><select name="month">{{range $i,$x:= (index $d "Professionals")}}{{end}}<option value="1">Janeiro</option><option value="2">Fevereiro</option><option value="3">Março</option><option value="4">Abril</option><option value="5">Maio</option><option value="6">Junho</option><option value="7">Julho</option><option value="8">Agosto</option><option value="9">Setembro</option><option value="10">Outubro</option><option value="11">Novembro</option><option value="12">Dezembro</option></select></div><div><label>Ano</label><input name="year" value="2026"></div></div><p><button>Validar todos da competência</button></p></form></section>{{end}}<section class="panel"><table><tr><th>Profissional</th><th>Data</th><th>Pagador</th><th>CPF</th><th>Valor</th><th>Status</th><th></th></tr>{{range index $d "Rows"}}<tr><td>{{.Professional}}</td><td>{{.PaymentDate}}</td><td>{{.PayerName}}</td><td>{{.PayerCPF}}</td><td>{{money .AmountCents}}</td><td><span class="tag">{{.Status}}</span></td><td>{{if eq .Status "PENDENTE_REVISAO"}}<form method="post" action="/payments/validate"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="professional_id" value="{{.ProfessionalID}}"><button>Validar</button></form>{{end}}</td></tr>{{else}}<tr><td colspan="7" class="muted">Nenhum lançamento.</td></tr>{{end}}</table></section>{{end}}`

const restrictionsHTML = `{{define "body"}}{{$d:=.Data}}<section class="panel"><p>Linhas com movimento e dados inválidos ficam preservadas aqui. Após correção, viram lançamento pendente de revisão.</p>{{range index $d "Rows"}}<div class="warn"><b>{{.Professional}}</b> — {{.SourceSheet}}!{{.SourceRow}} — {{.Reason}}<form method="post" action="/restrictions/resolve"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><div class="row"><div><label>Nome pagador</label><input name="payer_name" value="{{.PayerName}}" required></div><div><label>CPF pagador</label><input name="payer_cpf" value="{{.PayerCPFRaw}}" required></div><div><label>Data</label><input name="payment_date" value="{{.PaymentDateRaw}}" required></div><div><label>Valor</label><input name="amount" value="{{.AmountRaw}}" required></div><div><label><input style="width:auto" type="checkbox" name="same_person" checked> Pagador e beneficiário são a mesma pessoa</label></div><div><label>Nome beneficiário</label><input name="beneficiary_name" value="{{.BeneficiaryName}}"></div><div><label>CPF beneficiário</label><input name="beneficiary_cpf" value="{{.BeneficiaryRaw}}"></div></div><p><button>Corrigir e criar lançamento</button></p></form></div>{{else}}<p class="muted">Nenhuma pendência.</p>{{end}}</section>{{end}}`

const exportHTML = `{{define "body"}}{{$d:=.Data}}{{with index $d "Msg"}}{{if .}}<div class="warn">{{.}}</div>{{end}}{{end}}<section class="panel"><p>O CSV contém apenas lançamentos validados do profissional e da competência selecionados. Formato Receita Saúde: sem cabeçalho, separador ponto e vírgula, 16 campos.</p><form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Profissional</label><select name="professional_id" required><option value="">Selecione</option>{{range index $d "Professionals"}}<option value="{{.ID}}">{{.Name}} — {{.CPF}}</option>{{end}}</select><div class="row"><div><label>Mês</label><select name="month"><option value="1">Janeiro</option><option value="2">Fevereiro</option><option value="3">Março</option><option value="4">Abril</option><option value="5">Maio</option><option value="6">Junho</option><option value="7" selected>Julho</option><option value="8">Agosto</option><option value="9">Setembro</option><option value="10">Outubro</option><option value="11">Novembro</option><option value="12">Dezembro</option></select></div><div><label>Ano</label><input name="year" value="2026" required></div></div><p><button>Gerar escrituração CSV</button></p></form></section>{{end}}`

func syntheticJulyWorkbook()([]byte,error){
    f:=excelize.NewFile();defer f.Close();sheet:="Plan1";f.SetCellValue(sheet,"A1","Carnê Leão - Livro Caixa - 2026");f.SetCellValue(sheet,"A83","JULHO");f.SetCellValue(sheet,"A84","Nome");f.SetCellValue(sheet,"G84","CPF - Responsável");f.SetCellValue(sheet,"J84","CPF e Nome - Paciente - Se necessário");f.SetCellValue(sheet,"P84","Data");f.SetCellValue(sheet,"R84","Valor")
    amounts:=make([]float64,26);for i:=0;i<25;i++{amounts[i]=400};amounts[25]=870
    for i,v:=range amounts{row:=85+i;f.SetCellValue(sheet,fmt.Sprintf("A%d",row),fmt.Sprintf("Pagador Julho %d",i+1));f.SetCellValue(sheet,fmt.Sprintf("G%d",row),validCPF(i+1));f.SetCellValue(sheet,fmt.Sprintf("P%d",row),i+1);f.SetCellValue(sheet,fmt.Sprintf("R%d",row),v)}
    names:=[]string{"Henrique","Elisandra","Pâmela","Maria Fernanda"};for i,n:=range names{row:=111+i;f.SetCellValue(sheet,fmt.Sprintf("A%d",row),n);f.SetCellValue(sheet,fmt.Sprintf("G%d",row),validCPF(27+i))};f.SetCellValue(sheet,"A115","Valor Total Mês");f.SetCellValue(sheet,"R115",10870.00)
    var b bytes.Buffer;if err:=f.Write(&b);err!=nil{return nil,err};return b.Bytes(),nil
}

func singleRowWorkbook(name,cpf string,day int,amount float64)([]byte,error){f:=excelize.NewFile();defer f.Close();s:="Julho 2026";f.SetSheetName("Sheet1",s);f.SetCellValue(s,"A1","Nome Responsável");f.SetCellValue(s,"B1","CPF - Responsável");f.SetCellValue(s,"C1","Data");f.SetCellValue(s,"D1","Valor");f.SetCellValue(s,"A2",name);f.SetCellValue(s,"B2",cpf);f.SetCellValue(s,"C2",day);f.SetCellValue(s,"D2",amount);var b bytes.Buffer;if err:=f.Write(&b);err!=nil{return nil,err};return b.Bytes(),nil}

func validCPF(seed int)string{base:=fmt.Sprintf("%09d",100000000+seed);nums:=make([]int,9);for i:=0;i<9;i++{nums[i]=int(base[i]-'0')};sum:=0;for i:=0;i<9;i++{sum+=nums[i]*(10-i)};d1:=11-(sum%11);if d1>=10{d1=0};sum=0;for i:=0;i<9;i++{sum+=nums[i]*(11-i)};sum+=d1*2;d2:=11-(sum%11);if d2>=10{d2=0};return base+strconv.Itoa(d1)+strconv.Itoa(d2)}

func writeReport(path,text string){if path==""{return};_ = os.MkdirAll(filepath.Dir(path),0755);_ = os.WriteFile(path,[]byte(text),0644)}

func countAndTotal(db *sql.DB,prof int64)(int,int64,error){var c int;var t sql.NullInt64;err:=db.QueryRow(`SELECT COUNT(*),SUM(amount_cents) FROM payments WHERE professional_id=?`,prof).Scan(&c,&t);return c,t.Int64,err}

func exportRowsForTest(db *sql.DB,profID int64)([][]string,int64,error){
    var p Professional;var act int;if err:=db.QueryRow(`SELECT id,name,cpf,profession_key,registry,registry_state,active FROM professionals WHERE id=?`,profID).Scan(&p.ID,&p.Name,&p.CPF,&p.ProfessionKey,&p.Registry,&p.RegistryState,&act);err!=nil{return nil,0,err}
    rows,err:=db.Query(`SELECT id,professional_id,'',payer_name,payer_cpf,beneficiary_name,beneficiary_cpf,payment_date,amount_cents,status FROM payments WHERE professional_id=? AND status='VALIDADO_RECEITA_SAUDE' ORDER BY payment_date,id`,profID);if err!=nil{return nil,0,err};defer rows.Close();var out [][]string;var total int64;for rows.Next(){var r Payment;if err:=rows.Scan(&r.ID,&r.ProfessionalID,&r.Professional,&r.PayerName,&r.PayerCPF,&r.BeneficiaryName,&r.BeneficiaryCPF,&r.PaymentDate,&r.AmountCents,&r.Status);err!=nil{return nil,0,err};line,err:=csvRow(p,r);if err!=nil{return nil,0,err};out=append(out,line);total+=r.AmountCents};return out,total,rows.Err()
}

func runSelfTest(root,report string)error{
    _ = os.RemoveAll(root);a,err:=newApp(root);if err!=nil{return err}
    idA,err:=a.addProfessional("Psicóloga A","52998224725","PSICOLOGO","07/54321","RS");if err!=nil{return err};a.db.Close()
    a,err=newApp(root);if err!=nil{return err};defer a.db.Close();if _,err=a.professional(idA);err!=nil{return fmt.Errorf("profissional A não persistiu: %w",err)}
    raw,err:=syntheticJulyWorkbook();if err!=nil{return err};p,err:=parseWorkbook(raw);if err!=nil{return err};p.SourceName="julho-2026.xlsx";if len(p.Valid)!=26||len(p.Skipped)!=4||len(p.Restricted)!=0||len(p.Structural)!=0{return fmt.Errorf("prévia julho inválida: valid=%d skipped=%d restricted=%d structural=%d",len(p.Valid),len(p.Skipped),len(p.Restricted),len(p.Structural))};var sum int64;for _,r:=range p.Valid{sum+=r.AmountCents};if sum!=1087000{return fmt.Errorf("total julho=%d",sum)}
    c,rr,_,err:=a.importPreview(p,idA);if err!=nil||c!=26||rr!=0{return fmt.Errorf("importação A falhou c=%d r=%d err=%v",c,rr,err)};a.db.Close();a,err=newApp(root);if err!=nil{return err};defer a.db.Close();cc,tt,err:=countAndTotal(a.db,idA);if err!=nil||cc!=26||tt!=1087000{return fmt.Errorf("persistência A falhou %d/%d: %v",cc,tt,err)}
    idB,err:=a.addProfessional("Psicóloga B","16899535009","PSICOLOGO","07/65432","RS");if err!=nil{return err};braw,_:=singleRowWorkbook("Paciente Exclusivo B","11144477735",20,333);bp,err:=parseWorkbook(braw);if err!=nil||len(bp.Valid)!=1{return fmt.Errorf("prévia B falhou: %v valid=%d",err,len(bp.Valid))};bp.SourceName="b.xlsx";if c,_,_,err=a.importPreview(bp,idB);err!=nil||c!=1{return fmt.Errorf("importação B falhou: %v",err)}
    ca,ta,_:=countAndTotal(a.db,idA);cb,tb,_:=countAndTotal(a.db,idB);if ca!=26||ta!=1087000||cb!=1||tb!=33300{return fmt.Errorf("isolamento A/B falhou A=%d/%d B=%d/%d",ca,ta,cb,tb)}
    if err:=a.validateAll(idA,7,2026);err!=nil{return err};if err:=a.validateAll(idB,7,2026);err!=nil{return err};rowsA,totalA,err:=exportRowsForTest(a.db,idA);if err!=nil{return err};if len(rowsA)!=26||totalA!=1087000{return fmt.Errorf("export A inválido %d/%d",len(rowsA),totalA)};for _,row:=range rowsA{if len(row)!=16||row[14]!="52998224725"{return errors.New("CSV A misturou profissional ou não tem 16 campos")}};rowsB,totalB,err:=exportRowsForTest(a.db,idB);if err!=nil{return err};if len(rowsB)!=1||totalB!=33300||rowsB[0][14]!="16899535009"{return errors.New("CSV B inválido ou misturado")}
    inv,_:=singleRowWorkbook("Paciente CPF Inválido","12345678901",15,250);ip,err:=parseWorkbook(inv);if err!=nil||len(ip.Restricted)!=1||len(ip.Valid)!=0{return errors.New("CPF inválido não virou pendência")};ip.SourceName="cpf-invalido.xlsx";if _,r,_,err:=a.importPreview(ip,idA);err!=nil||r!=1{return fmt.Errorf("persistência da pendência falhou: %v",err)};var rc int;if err:=a.db.QueryRow(`SELECT COUNT(*) FROM import_restrictions WHERE professional_id=? AND status='PENDENTE'`,idA).Scan(&rc);err!=nil||rc!=1{return errors.New("pendência não persistiu")}
    text:=fmt.Sprintf("MVP_ACCEPTANCE_OK\nprofessional_a_persisted=ok\njuly_imported=26\njuly_skipped_no_service=4\njuly_total_cents=1087000\nprofessional_b_isolation=ok\nexport_a_rows=26\nexport_a_fields=16\nexport_a_total_cents=1087000\nexport_b_isolation=ok\ninvalid_cpf_restriction=ok\n")
    writeReport(report,text);return nil
}

func runSeedReinstall(root,report string)error{
    _ = os.RemoveAll(root);a,err:=newApp(root);if err!=nil{return err};defer a.db.Close();id,err:=a.addProfessional("Psicóloga Reinstalação","52998224725","PSICOLOGO","07/80001","RS");if err!=nil{return err};raw,_:=syntheticJulyWorkbook();p,err:=parseWorkbook(raw);if err!=nil{return err};p.SourceName="reinstall.xlsx";c,_,_,err:=a.importPreview(p,id);if err!=nil||c!=26{return fmt.Errorf("seed import falhou: %v",err)};writeReport(report,"SEED_REINSTALL_OK\nrows=26\ntotal_cents=1087000\n");return nil
}
func runVerifyReinstall(root,report string)error{a,err:=newApp(root);if err!=nil{return err};defer a.db.Close();var id int64;if err:=a.db.QueryRow(`SELECT id FROM professionals WHERE cpf='52998224725'`).Scan(&id);err!=nil{return errors.New("profissional desapareceu após reinstalação")};c,t,err:=countAndTotal(a.db,id);if err!=nil||c!=26||t!=1087000{return fmt.Errorf("SQLite não persistiu após reinstalação: %d/%d",c,t)};writeReport(report,"REINSTALL_PERSISTENCE_OK\nprofessional=ok\nrows=26\ntotal_cents=1087000\n");return nil}
