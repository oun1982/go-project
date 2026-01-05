package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// execShell runs a shell command with a 30s timeout and returns trimmed stdout.
func execShell(cmd string) string {
	c := exec.Command("bash", "-lc", cmd)
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out

	done := make(chan error, 1)
	go func() { done <- c.Run() }()

	select {
	case <-time.After(30 * time.Second):
		_ = c.Process.Kill()
		return ""
	case <-done:
		return strings.TrimSpace(out.String())
	}
}

// networkInterface represents a single interface entry.
type networkInterface struct {
	Name   string
	IP     string
	Status string
	MAC    string
}

// getNetworkInterfaces returns numbered interfaces skipping lo/docker0.
func getNetworkInterfaces() map[string]networkInterface {
	result := make(map[string]networkInterface)

	output := execShell("ip -j a")
	if strings.HasPrefix(output, "[") { // JSON path
		var data []struct {
			IfName    string `json:"ifname"`
			OperState string `json:"operstate"`
			Address   string `json:"address"`
			AddrInfo  []struct {
				Family    string `json:"family"`
				Local     string `json:"local"`
				PrefixLen int    `json:"prefixlen"`
			} `json:"addr_info"`
		}
		if err := json.Unmarshal([]byte(output), &data); err == nil {
			idx := 0
			skip := map[string]bool{"lo": true, "docker0": true}
			for _, iface := range data {
				if skip[iface.IfName] {
					continue
				}
				ip := ""
				for _, addr := range iface.AddrInfo {
					if addr.Family == "inet" {
						ip = fmt.Sprintf("%s/%d", addr.Local, addr.PrefixLen)
						break
					}
				}
				if ip == "" {
					continue
				}
				idx++
				key := fmt.Sprintf("iface%d", idx)
				result[key] = networkInterface{
					Name:   iface.IfName,
					IP:     ip,
					Status: iface.OperState,
					MAC:    iface.Address,
				}
			}
			return result
		}
	}

	// Fallback for older ip without JSON support (e.g., CentOS 6).
	output = execShell("ip -o -4 addr show | awk '{print $2\" \" $4}'")
	if output == "" {
		return result
	}
	lines := strings.Split(output, "\n")
	idx := 0
	skip := map[string]bool{"lo": true, "docker0": true}
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		if skip[name] {
			continue
		}
		ip := parts[1]
		if ip == "" {
			continue
		}
		idx++
		key := fmt.Sprintf("iface%d", idx)
		result[key] = networkInterface{
			Name:   name,
			IP:     ip,
			Status: "UP",
			MAC:    "",
		}
	}
	return result
}

// cpuInfo holds CPU model and percentages.
type cpuInfo struct {
	Model string
	US    string
	SY    string
	NI    string
	ID    string
}

func getCPUInfo() cpuInfo {
	model := execShell("cat /proc/cpuinfo | grep 'model name' | head -1 | awk -F': ' '{print $2}'")
	topOutput := execShell("top -bn1 | grep 'Cpu'")
	stats := cpuInfo{Model: model, US: "0", SY: "0", NI: "0", ID: "0"}
	if topOutput != "" {
		re := regexp.MustCompile(`([\d.]+)\s+us.*?([\d.]+)\s+sy.*?([\d.]+)\s+ni.*?([\d.]+)\s+id`)
		m := re.FindStringSubmatch(topOutput)
		if len(m) == 5 {
			stats.US, stats.SY, stats.NI, stats.ID = m[1], m[2], m[3], m[4]
		}
	}
	return stats
}

// memoryInfo holds memory and swap stats.
type memoryInfo struct {
	MemTotal  string
	MemUsed   string
	MemFree   string
	SwapTotal string
	SwapUsed  string
	SwapFree  string
	Memory    string
}

func getMemoryInfo() memoryInfo {
	info := memoryInfo{}
	freeOutput := execShell("free -h")
	lines := strings.Split(freeOutput, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Mem:") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				info.MemTotal = parts[1]
				info.MemUsed = parts[2]
				info.MemFree = parts[3]
			}
		}
		if strings.HasPrefix(line, "Swap:") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				info.SwapTotal = parts[1]
				info.SwapUsed = parts[2]
				info.SwapFree = parts[3]
			}
		}
	}

	dmi := execShell("dmidecode -t 17 | grep Size")
	if dmi != "" {
		var sizes []string
		for _, line := range strings.Split(dmi, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.Contains(line, "Size:") {
				parts := strings.SplitN(line, "Size:", 2)
				if len(parts) == 2 {
					sizes = append(sizes, strings.TrimSpace(parts[1]))
				}
			}
		}
		if len(sizes) > 0 {
			info.Memory = strings.Join(sizes, ", ")
		}
	}
	return info
}

// getServiceStatus returns text between Active: and since, else not found.
func getServiceStatus(name string) string {
	out := execShell(fmt.Sprintf("systemctl status %s | grep Active", name))
	if out == "" {
		return "not found"
	}
	re := regexp.MustCompile(`Active:\s*(.+?)\s+since`)
	m := re.FindStringSubmatch(out)
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return "not found"
}

// diskInfo entry.
type diskInfo struct {
	FileSystem string
	Size       string
	Used       string
	Avail      string
	UsePercent string
	Mount      string
}

func getDiskInfo() map[string]diskInfo {
	result := make(map[string]diskInfo)
	out := execShell("df -h")
	lines := strings.Split(out, "\n")
	idx := 0
	for _, line := range lines[1:] { // skip header
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 6 {
			continue
		}
		fs := parts[0]
		skip := []string{"tmpfs", "devtmpfs", "udev", "/run", "/dev/loop"}
		skipIt := false
		for _, s := range skip {
			if strings.Contains(fs, s) {
				skipIt = true
				break
			}
		}
		if skipIt {
			continue
		}
		idx++
		key := fmt.Sprintf("disk%d", idx)
		result[key] = diskInfo{
			FileSystem: fs,
			Size:       parts[1],
			Used:       parts[2],
			Avail:      parts[3],
			UsePercent: parts[4],
			Mount:      parts[5],
		}
	}
	return result
}

// getRoutes parses route -n up to 20 entries.
func getRoutes() [][]string {
	out := execShell("route -n")
	lines := strings.Split(out, "\n")
	var routes [][]string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Kernel") || strings.HasPrefix(line, "Destination") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		routes = append(routes, []string{parts[0], parts[1], parts[2]})
		if len(routes) >= 20 {
			break
		}
	}
	return routes
}

// buildContext builds placeholder->value map for replacement.
func buildContext() map[string]string {
	ctx := make(map[string]string)

	network := getNetworkInterfaces()
	disks := getDiskInfo()
	routes := getRoutes()
	cpu := getCPUInfo()
	mem := getMemoryInfo()

	hostname := execShell("hostname")
	model := execShell("cat /sys/class/dmi/id/product_name 2>/dev/null || echo 'N/A'")
	serial := execShell("cat /etc/machine-id 2>/dev/null || uuidgen || echo 'Not Specified'")
	osVersion := execShell("uname -r")

	add := func(key, val string) {
		// Support both {{key}} and {{ key }}
		ctx["{{"+key+"}}"] = val
		ctx["{{ "+key+" }}"] = val
	}

	add("hostname", hostname)
	add("model", model)
	add("sv_serial", serial)
	add("os_version", osVersion)
	add("sv_cpu", cpu.Model)
	add("sv_cpu_usage", "")
	add("us", cpu.US)
	add("sy", cpu.SY)
	add("ni", cpu.NI)
	add("id", cpu.ID)
	add("mem_total", mem.MemTotal)
	add("mem_used", mem.MemUsed)
	add("mem_free", mem.MemFree)
	add("swap_total", mem.SwapTotal)
	add("swap_used", mem.SwapUsed)
	add("swap_free", mem.SwapFree)
	add("memory", mem.Memory)

	services := map[string]string{
		"cron":         "crond",
		"apache":       "apache2",
		"mysql":        "mariadb",
		"chrony":       "chrony",
		"asterisk":     "asterisk",
		"dblockclient": "dblockclient",
		"dcall":        "dcall",
		"dblock":       "dblock",
	}
	for k, svc := range services {
		add(k, getServiceStatus(svc))
	}

	// iface placeholders iface1..iface5 with [0-3]
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("iface%d", i)
		iface, ok := network[key]
		vals := []string{"", "", "", ""}
		if ok {
			vals = []string{iface.Name, iface.IP, iface.Status, iface.MAC}
		}
		for j := 0; j < 4; j++ {
			add(fmt.Sprintf("iface%d[%d]", i, j), vals[j])
		}
	}

	// disk array placeholders disk1..disk10 with [0-5]
	for i := 1; i <= 10; i++ {
		key := fmt.Sprintf("disk%d", i)
		d, ok := disks[key]
		vals := []string{"", "", "", "", "", ""}
		if ok {
			vals = []string{d.FileSystem, d.Size, d.Used, d.Avail, strings.TrimSuffix(d.UsePercent, "%"), d.Mount}
		}
		for j := 0; j < 6; j++ {
			add(fmt.Sprintf("disk%d[%d]", i, j), vals[j])
		}
		// flat disk_n placeholders
		if ok {
			add(fmt.Sprintf("disk%d_partition", i), d.FileSystem)
			add(fmt.Sprintf("disk%d_size", i), strings.TrimRight(d.Size, "KMGT"))
			add(fmt.Sprintf("disk%d_used", i), strings.TrimRight(d.Used, "KMGT"))
			add(fmt.Sprintf("disk%d_avail", i), strings.TrimRight(d.Avail, "KMGT"))
			add(fmt.Sprintf("disk%d_use_percent", i), strings.TrimSuffix(d.UsePercent, "%"))
			add(fmt.Sprintf("disk%d_mounted_on", i), d.Mount)
		} else {
			add(fmt.Sprintf("disk%d_partition", i), "")
			add(fmt.Sprintf("disk%d_size", i), "")
			add(fmt.Sprintf("disk%d_used", i), "")
			add(fmt.Sprintf("disk%d_avail", i), "")
			add(fmt.Sprintf("disk%d_use_percent", i), "")
			add(fmt.Sprintf("disk%d_mounted_on", i), "")
		}
	}

	// routes route1..route20 [0-2]
	for i := 1; i <= 20; i++ {
		vals := []string{"", "", ""}
		if i-1 < len(routes) {
			vals = routes[i-1]
		}
		for j := 0; j < 3; j++ {
			add(fmt.Sprintf("route%d[%d]", i, j), vals[j])
		}
	}

	return ctx
}

// cleanXMLPlaceholders removes XML tags between {{ and }} to fix broken placeholders
func cleanXMLPlaceholders(s string) string {
	// Remove </w:t> and <w:t> (and similar tags) between {{ and }}
	re := regexp.MustCompile(`\{\{([^}]*?)\}\}`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		// Remove all XML tags inside the placeholder
		cleaned := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(match, "")
		// Remove extra whitespace
		cleaned = regexp.MustCompile(`\s+`).ReplaceAllString(cleaned, " ")
		cleaned = strings.TrimSpace(cleaned)
		// Keep only {{ content }}
		content := strings.TrimPrefix(cleaned, "{{")
		content = strings.TrimSuffix(content, "}}")
		content = strings.TrimSpace(content)
		return "{{" + content + "}}"
	})
}

// renderDocx copies template and replaces placeholders in XML parts.
func renderDocx(templatePath, outputPath string, ctx map[string]string) error {
	tmpl, err := zip.OpenReader(templatePath)
	if err != nil {
		return err
	}
	defer tmpl.Close()

	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	w := zip.NewWriter(outFile)
	defer w.Close()

	for _, f := range tmpl.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := ioutil.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}

		// Only attempt replacements on XML files.
		if strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			s := string(data)

			// Clean XML tags from placeholders first
			s = cleanXMLPlaceholders(s)

			// Now do replacements
			for k, v := range ctx {
				s = strings.ReplaceAll(s, k, v)
			}
			data = []byte(s)
		}

		header := &zip.FileHeader{
			Name:   f.Name,
			Method: f.Method,
		}
		header.SetModTime(time.Now())
		header.SetMode(f.Mode())
		fw, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := fw.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	ctx := buildContext()

	hostname := execShell("hostname")
	now := time.Now().Format("20060102_150405")
	output := fmt.Sprintf("output_%s_%s.docx", hostname, now)

	templatePath := "template_flexible.docx"
	if _, err := os.Stat(templatePath); err != nil {
		fmt.Println("template file not found:", templatePath)
		os.Exit(1)
	}

	if err := renderDocx(templatePath, output, ctx); err != nil {
		fmt.Println("failed to render docx:", err)
		os.Exit(1)
	}

	fmt.Println("Saved as:", output)

	// Print summaries similar to Python script.
	fmt.Println("ข้อมูล Network Interfaces:")
	for i := 1; i <= 5; i++ {
		n := fmt.Sprintf("iface%d", i)
		fmt.Printf("  %s: %s | %s | %s | %s\n", n, ctx[fmt.Sprintf("{{ iface%d[0] }}", i)], ctx[fmt.Sprintf("{{ iface%d[1] }}", i)], ctx[fmt.Sprintf("{{ iface%d[2] }}", i)], ctx[fmt.Sprintf("{{ iface%d[3] }}", i)])
	}

	fmt.Println("\nข้อมูล Disk:")
	for i := 1; i <= 10; i++ {
		fmt.Printf("  disk%d: %s | Size: %s | Used: %s | Avail: %s | Use%%: %s | Mount: %s\n",
			i,
			ctx[fmt.Sprintf("{{ disk%d[0] }}", i)],
			ctx[fmt.Sprintf("{{ disk%d[1] }}", i)],
			ctx[fmt.Sprintf("{{ disk%d[2] }}", i)],
			ctx[fmt.Sprintf("{{ disk%d[3] }}", i)],
			ctx[fmt.Sprintf("{{ disk%d[4] }}", i)],
			ctx[fmt.Sprintf("{{ disk%d[5] }}", i)],
		)
	}

	fmt.Println("\nข้อมูล Memory:")
	fmt.Printf("  Mem Total: %s | Used: %s | Free: %s\n", ctx["{{ mem_total }}"], ctx["{{ mem_used }}"], ctx["{{ mem_free }}"])
	fmt.Printf("  Swap Total: %s | Used: %s | Free: %s\n", ctx["{{ swap_total }}"], ctx["{{ swap_used }}"], ctx["{{ swap_free }}"])

	fmt.Println("\nข้อมูล Routes:")
	for i := 1; i <= 20; i++ {
		fmt.Printf("  route%d: %s | %s | %s\n", i, ctx[fmt.Sprintf("{{ route%d[0] }}", i)], ctx[fmt.Sprintf("{{ route%d[1] }}", i)], ctx[fmt.Sprintf("{{ route%d[2] }}", i)])
	}

	fmt.Println("สร้างไฟล์ Word สำเร็จ!")
}
