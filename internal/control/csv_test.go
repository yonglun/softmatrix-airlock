package control

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/usage"
)

func TestWriteCSVHasBOMAndAttachmentHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCSV(rec, "x.csv", []string{"维度", "成本(元)"}, [][]string{{"rd", "0.10"}})

	require.Contains(t, rec.Header().Get("Content-Type"), "text/csv")
	require.Contains(t, rec.Header().Get("Content-Disposition"), `filename="x.csv"`)

	body := rec.Body.Bytes()
	require.Equal(t, []byte{0xEF, 0xBB, 0xBF}, body[:3],
		"没有 UTF-8 BOM 时 Excel 会把中文列名读成乱码，而这份导出的主要去处就是 Excel")

	text := string(body[3:])
	require.Equal(t, "维度,成本(元)\nrd,0.10\n", strings.ReplaceAll(text, "\r\n", "\n"))
}

func TestSummaryCSVRowsFormatsMoneyAsYuan(t *testing.T) {
	// 1 Micro = 1e-6 元。导出的是给人看的金额，不是内部单位。
	rows := summaryCSVRows([]usage.SummaryRow{
		{Key: "rd", Requests: 2, InputTokens: 10, OutputTokens: 20, CostMicro: 1_500_000},
	})
	require.Equal(t, []string{"rd", "2", "10", "20", "1.500000"}, rows[0])
}

func TestAuditCSVRowsFormatsMoneyAsYuan(t *testing.T) {
	rows := auditCSVRows([]usage.AuditRow{{
		RequestID: "r1", OrgID: "rd", UserID: "alice", KeyID: "k1",
		Model: "qwen-plus", StatusCode: 200, LatencyMS: 120, TTFTMS: 30,
		InputTokens: 10, OutputTokens: 20, CostMicro: 2_000_000, ErrorType: "",
	}})
	require.Equal(t, "2.000000", rows[0][11])
}
