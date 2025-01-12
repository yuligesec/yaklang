package rules

import (
	"github.com/yaklang/yaklang/common/yak/plugin_type_analyzer"
	"github.com/yaklang/yaklang/common/yak/ssaapi"
	"github.com/yaklang/yaklang/common/yakgrpc/yakit"
)

func init() {
	plugin_type_analyzer.RegisterTypeInfoCollector("yak", CliTypeInfo)
	plugin_type_analyzer.RegisterTypeInfoCollector("yak", RiskTypeInfo)
}

func CliTypeInfo(prog *ssaapi.Program) *plugin_type_analyzer.YaklangInfo {
	ret := plugin_type_analyzer.NewYakLangInfo("cli")
	for _, param := range ParseCliParameter(prog) {
		ret.AddKV(CliParameterToInformation(param))
	}
	return ret
}

func RiskTypeInfo(prog *ssaapi.Program) *plugin_type_analyzer.YaklangInfo {
	ret := plugin_type_analyzer.NewYakLangInfo("risk")
	for _, risk := range ParseRiskInfo(prog) {
		ret.AddKV(RiskInfoToInformation(risk))
	}
	return ret
}

type CliParameter struct {
	Name     string
	Type     string
	Help     string
	Required bool
	Default  any
}

func newCliParameter(typ, name string) *CliParameter {
	return &CliParameter{
		Name:     name,
		Type:     typ,
		Help:     "",
		Required: false,
		Default:  nil,
	}
}

func ParseCliParameter(prog *ssaapi.Program) []*CliParameter {
	// prog.Show()
	ret := make([]*CliParameter, 0)

	getConstString := func(v *ssaapi.Value) string {
		if str, ok := v.GetConstValue().(string); ok {
			return str
		}
		return ""
	}

	handleOption := func(cli *CliParameter, opt *ssaapi.Value) {
		// opt.ShowUseDefChain()
		if !opt.IsCall() {
			// skip no function call
			return
		}

		// check option function, get information
		switch opt.GetOperand(0).String() {
		case "cli.setHelp":
			cli.Help = getConstString(opt.GetOperand(1))
		case "cli.setRequired":
			cli.Required = getConstString(opt.GetOperand(1)) == "true"
		case "cli.setDefault":
			cli.Default = opt.GetOperand(1).GetConstValue()
		}
	}

	parseCliFunction := func(funName, typName string) {
		prog.Ref(funName).GetUsers().Filter(
			func(v *ssaapi.Value) bool {
				// only function call and must be reachable
				return v.IsCall() && v.IsReachable() != -1
			},
		).ForEach(func(v *ssaapi.Value) {
			// cli.String("arg1", opt...)
			// op(0) => cli.String
			// op(1) => "arg1"
			// op(2...) => opt
			name := v.GetOperand(1).String()
			if v.GetOperand(1).IsConstInst() {
				name = v.GetOperand(1).GetConstValue().(string)
			}
			cli := newCliParameter(typName, name)
			opLen := len(v.GetOperands())
			// handler option
			for i := 2; i < opLen; i++ {
				handleOption(cli, v.GetOperand(i))
			}
			ret = append(ret, cli)
		})
	}

	parseCliFunction("cli.String", "string")
	parseCliFunction("cli.Bool", "bool")
	parseCliFunction("cli.Int", "int")
	parseCliFunction("cli.Integer", "int")
	parseCliFunction("cli.Double", "float")
	parseCliFunction("cli.Float", "float")
	parseCliFunction("cli.Url", "urls")
	parseCliFunction("cli.Urls", "urls")
	parseCliFunction("cli.Port", "port")
	parseCliFunction("cli.Ports", "port")
	parseCliFunction("cli.Net", "hosts")
	parseCliFunction("cli.Network", "hosts")
	parseCliFunction("cli.Host", "hosts")
	parseCliFunction("cli.Hosts", "hosts")
	parseCliFunction("cli.File", "file")
	parseCliFunction("cli.FileOrContent", "file_or_content")
	parseCliFunction("cli.LineDict", "file-or-content")
	parseCliFunction("cli.YakitPlugin", "yakit-plugin")
	parseCliFunction("cli.StringSlice", "string-slice")

	return ret
}

type RiskInfo struct {
	Level             string
	CVE               string
	Type, TypeVerbose string
}

func newRiskInfo() *RiskInfo {
	return &RiskInfo{
		Level:       "",
		CVE:         "",
		Type:        "",
		TypeVerbose: "",
	}
}

func ParseRiskInfo(prog *ssaapi.Program) []*RiskInfo {
	ret := make([]*RiskInfo, 0)
	getConstString := func(v *ssaapi.Value) string {
		if v.IsConstInst() {
			if str, ok := v.GetConstValue().(string); ok {
				return str
			}
		}
		// TODO: handler value with other opcode
		return ""
	}

	handleRiskLevel := func(level string) string {
		switch level {
		case "high":
			return "high"
		case "critical", "panic", "fatal":
			return "critical"
		case "warning", "warn", "middle", "medium":
			return "warning"
		case "info", "debug", "trace", "fingerprint", "note", "fp":
			return "info"
		case "low", "default":
			return "low"
		default:
			return "low"
		}
	}

	handleOption := func(riskInfo *RiskInfo, call *ssaapi.Value) {
		if !call.IsCall() {
			return
		}
		switch call.GetOperand(0).String() {
		case "risk.severity", "risk.level":
			riskInfo.Level = handleRiskLevel(getConstString(call.GetOperand(1)))
		case "risk.cve":
			riskInfo.CVE = call.GetOperand(1).String()
		case "risk.type":
			riskInfo.Type = getConstString(call.GetOperand(1))
			riskInfo.TypeVerbose = yakit.RiskTypeToVerbose(riskInfo.Type)
		case "risk.typeVerbose":
			riskInfo.TypeVerbose = getConstString(call.GetOperand(1))
		}
	}

	parseRiskFunction := func(name string, OptIndex int) {
		prog.Ref(name).GetUsers().Filter(func(v *ssaapi.Value) bool {
			return v.IsCall() && v.IsReachable() != -1
		}).ForEach(func(v *ssaapi.Value) {
			riskInfo := newRiskInfo()
			optLen := len(v.GetOperands())
			for i := OptIndex; i < optLen; i++ {
				handleOption(riskInfo, v.GetOperand(i))
			}
			ret = append(ret, riskInfo)
		})
	}

	parseRiskFunction("risk.CreateRisk", 1)
	parseRiskFunction("risk.NewRisk", 1)

	return ret
}

func CliParameterToInformation(c *CliParameter) *plugin_type_analyzer.YaklangInfoKV {
	ret := plugin_type_analyzer.NewYaklangInfoKV("Name", c.Name)
	ret.AddExtern("Type", c.Type)
	ret.AddExtern("Help", c.Help)
	ret.AddExtern("Required", c.Required)
	ret.AddExtern("Default", c.Default)
	return ret
}

func RiskInfoToInformation(r *RiskInfo) *plugin_type_analyzer.YaklangInfoKV {
	ret := plugin_type_analyzer.NewYaklangInfoKV("Name", "risk")
	ret.AddExtern("Level", r.Level)
	ret.AddExtern("CVE", r.CVE)
	ret.AddExtern("Type", r.Type)
	ret.AddExtern("TypeVerbose", r.TypeVerbose)
	return ret
}
