package control

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/softmatrix/airlock/internal/usage"
)

// writeCSV 输出一份带附件头的 CSV。
//
// 开头写 UTF-8 BOM 是刻意的：不带它，Excel 会用本地代码页读文件，
// 中文列名直接变乱码——而这份导出的主要去处就是 Excel。
func writeCSV(w http.ResponseWriter, filename string, header []string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	for _, r := range rows {
		_ = cw.Write(r)
	}
	cw.Flush()
}

// yuan 把 Micro 格式化成元。1 Micro = 1e-6 元。
//
// 固定六位小数、不做四舍五入：导出是给人对账用的，任何一次提前舍入
// 都会让加总对不上明细。
func yuan(micro int64) string {
	return fmt.Sprintf("%d.%06d", micro/1_000_000, abs64(micro%1_000_000))
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func summaryCSVRows(rows []usage.SummaryRow) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.Key,
			strconv.FormatInt(r.Requests, 10),
			strconv.FormatInt(r.InputTokens, 10),
			strconv.FormatInt(r.OutputTokens, 10),
			yuan(r.CostMicro),
		})
	}
	return out
}

func auditCSVRows(rows []usage.AuditRow) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.Timestamp.Format(time.RFC3339),
			r.RequestID, r.OrgID, r.UserID, r.KeyID, r.Model,
			strconv.Itoa(r.StatusCode),
			strconv.Itoa(r.LatencyMS),
			strconv.Itoa(r.TTFTMS),
			strconv.FormatInt(r.InputTokens, 10),
			strconv.FormatInt(r.OutputTokens, 10),
			yuan(r.CostMicro),
			r.ErrorType,
		})
	}
	return out
}
