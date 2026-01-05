package main

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"
)

// Global reader เพื่อให้อ่าน stdin ต่อเนื่องกันได้โดยไม่เสีย buffer
var reader *bufio.Reader

func init() {
	reader = bufio.NewReader(os.Stdin)
	// Seed random number generator (จำเป็นสำหรับ Go < 1.20)
	rand.Seed(time.Now().UnixNano())
}

func readAGIEnv() {
	// อ่าน AGI env ให้หมดจนเจอบรรทัดว่าง
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
}

func agiCmd(cmd string) string {
	// เขียนคำสั่งไปยัง stdout
	fmt.Printf("%s\n", cmd)
	// fmt.Printf ใน Go ปกติจะ flush ให้ถ้าเป็น stdout แต่เพื่อความชัวร์ในบาง env
	// เราปล่อยให้ fmt จัดการ (AGI ปกติรับ newline เป็นตัว trigger)

	// อ่าน response บรรทัดเดียว
	resp, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(resp)
}

func agiVerbose(msg string) {
	safe := strings.ReplaceAll(msg, "\"", "'") // กัน quote แตก
	agiCmd(fmt.Sprintf("VERBOSE \"%s\" 1", safe))
}

func agiExec(app, args string) {
	agiCmd(fmt.Sprintf("EXEC %s %s", app, args))
}

func agiGetVar(variable string) string {
	res := agiCmd(fmt.Sprintf("GET VARIABLE %s", variable))
	// ตัวอย่าง: 200 result=1 (ANSWER)
	// หา index ของวงเล็บ
	start := strings.Index(res, "(")
	end := strings.Index(res, ")")

	if start != -1 && end > start {
		return res[start+1 : end]
	}
	return ""
}

func main() {
	// 1) อ่าน AGI environment ก่อน (ห้ามข้าม)
	readAGIEnv()

	// 2) รับ ringgroups จาก argv
	args := os.Args[1:]
	if len(args) < 1 {
		agiVerbose("No Ring Group arguments from dialplan (RG_LIST empty?)")
		return
	}

	// ใช้ map เพื่อทำ Deduplicate (กันซ้ำ)
	uniqueMap := make(map[string]bool)
	var ringGroups []string

	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed != "" {
			if _, exists := uniqueMap[trimmed]; !exists {
				uniqueMap[trimmed] = true
				ringGroups = append(ringGroups, trimmed)
			}
		}
	}

	if len(ringGroups) == 0 {
		agiVerbose("RG_LIST parsed empty after cleanup")
		return
	}

	agiVerbose(fmt.Sprintf("Ring Groups: %s", strings.Join(ringGroups, ",")))

	// 3) shuffle เพื่อ random แบบไม่ซ้ำ
	rand.Shuffle(len(ringGroups), func(i, j int) {
		ringGroups[i], ringGroups[j] = ringGroups[j], ringGroups[i]
	})

	// 4) วน dial ทีละ RG
	for _, rg := range ringGroups {
		agiVerbose(fmt.Sprintf("Dial Ring Group %s", rg))

		// สำคัญ: ผ่าน from-internal เพื่อให้ FreePBX recording/CDR ทำงานปกติ
		// Dial timeout 10 วินาทีตามต้นฉบับ Rust
		agiExec("Dial", fmt.Sprintf("Local/%s@from-internal,10", rg))

		status := agiGetVar("DIALSTATUS")
		agiVerbose(fmt.Sprintf("DIALSTATUS=%s", status))

		if status == "ANSWER" {
			agiVerbose(fmt.Sprintf("Answered by RG %s", rg))
			return
		}
		// ไม่ ANSWER => ไปตัวถัดไป
	}

	agiVerbose("No agent answered (all RG tried)")
}