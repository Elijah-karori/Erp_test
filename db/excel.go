package db

import (
	"fmt"
	"time"

	"github.com/xuri/excelize/v2"
)

// GenerateAuditExcel exports the current SQLite logs to an Excel stream
func GenerateAuditExcel(logs []DBLog) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	sheetName := "ERP Audit Logs"
	index, err := f.NewSheet(sheetName)
	if err != nil {
		return nil, err
	}
	f.SetActiveSheet(index)
	_ = f.DeleteSheet("Sheet1") // remove default sheet

	// Write headers
	headers := []string{"Log ID", "Tenant ID", "User ID", "ActionPerformed", "Details", "Timestamp"}
	for colIdx, header := range headers {
		cell, _ := excelize.CoordinatesToCellName(colIdx+1, 1)
		_ = f.SetCellValue(sheetName, cell, header)
	}

	// Write logs
	for rowIdx, logItem := range logs {
		r := rowIdx + 2
		_ = f.SetCellValue(sheetName, fmt.Sprintf("A%d", r), logItem.ID)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("B%d", r), logItem.TenantID)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("C%d", r), logItem.UserID)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("D%d", r), logItem.Action)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("E%d", r), logItem.Details)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("F%d", r), logItem.Timestamp.Format(time.RFC3339))
	}

	// Stylize headers
	style, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Bold:  true,
			Color: "FFFFFF",
		},
		Fill: excelize.Fill{
			Type:    "pattern",
			Color:   []string{"2F4F4F"}, // Dark Slate Gray (Precision Tonalism)
			Pattern: 1,
		},
	})
	if err == nil {
		_ = f.SetCellStyle(sheetName, "A1", "F1", style)
	}

	// Auto-fit columns
	_ = f.SetColWidth(sheetName, "A", "F", 25)

	// Save to buffer
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
