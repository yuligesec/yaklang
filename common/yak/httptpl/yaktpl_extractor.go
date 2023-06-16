package httptpl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xmlquery"
	"github.com/asaskevich/govalidator"
	"github.com/bcicen/jstream"
	"github.com/gobwas/httphead"
	"github.com/itchyny/gojq"
	"github.com/yaklang/yaklang/common/jsonextractor"
	"github.com/yaklang/yaklang/common/log"
	"github.com/yaklang/yaklang/common/utils"
	"github.com/yaklang/yaklang/common/utils/lowhttp"
	"github.com/yaklang/yaklang/common/yakgrpc/ypb"
	"mime"
	"net/url"
	"regexp"
	"strings"
)

func NewExtractorFromGRPCModel(m *ypb.HTTPResponseExtractor) *YakExtractor {
	return &YakExtractor{
		Name:             m.GetName(),
		Type:             m.GetType(),
		Scope:            m.GetScope(),
		Groups:           m.GetGroups(),
		RegexpMatchGroup: utils.Int64SliceToIntSlice(m.GetRegexpMatchGroup()),
		XPathAttribute:   m.GetXPathAttribute(),
	}
}

type YakExtractor struct {
	Name string // name or index

	// regexp
	// json
	// kval
	// xpath
	// nuclei-dsl
	Type string

	// body
	// header
	// all
	Scope            string // header body all
	Groups           []string
	RegexpMatchGroup []int
	XPathAttribute   string
}

// group1 for key
// group2 for value
var kvExtractorRegexp = regexp.MustCompile(`([^\s=:,]+)\s*((:)|(=))\s*?(\S[^\n\r]*)`)

func (y *YakExtractor) Execute(rsp []byte) (map[string]interface{}, error) {
	var material string
	switch strings.TrimSpace(strings.ToLower(y.Scope)) {
	case "body":
		_, body := lowhttp.SplitHTTPHeadersAndBodyFromPacket(rsp)
		material = string(body)
	case "header":
		header, _ := lowhttp.SplitHTTPHeadersAndBodyFromPacket(rsp)
		material = header
	default:
		material = string(rsp)
	}
	var results = []string{}
	addResult := func(result interface{}) {
		results = append(results, utils.InterfaceToString(result))
	}
	t := strings.TrimSpace(strings.ToLower(y.Type))
	switch t {
	case "regex":
		for _, group := range y.Groups {
			if group != "" {
				r, err := regexp.Compile(group)
				if err != nil {
					log.Errorf("compile[%v] failed: %v", group, err)
					continue
				}
				// default match group 0
				if len(y.RegexpMatchGroup) == 0 {
					y.RegexpMatchGroup = []int{0}
				}
				// just append result which in match group
				for _, res := range r.FindAllStringSubmatch(material, -1) {
					for _, i := range y.RegexpMatchGroup {
						if len(res) > i {
							addResult(res[i])
						}
					}
				}
			}
		}
	case "kv", "key-value", "kval":
		kvResult := ExtractKValFromResponse([]byte(material))
		for _, group := range y.Groups {
			if v, ok := kvResult[group]; ok {
				v1 := v.([]interface{})
				for _, v2 := range v1 {
					addResult(v2)
				}
			}
		}
	case "json", "jq":
		for _, group := range y.Groups {
			if group != "" {
				query, err := gojq.Parse(group)
				if err != nil {
					log.Errorf("parse jq query[%v] failed: %s", group, err)
					continue
				}
				var obj interface{}
				if err := json.Unmarshal([]byte(material), &obj); err != nil {
					log.Errorf("parse json failed: %s", err)
					continue
				}
				iter := query.Run(obj)
				for {
					v, ok := iter.Next()
					if !ok {
						break
					}
					if err, ok := v.(error); ok {
						log.Warnf("jq query[%v] failed: %s", group, err)
						continue
					}
					addResult(v)
				}
			}
		}
	case "xpath":
		isXml := strings.HasPrefix(strings.ToLower(strings.TrimSpace(material)), "<?xml")
	TRYXML:
		if isXml {
			doc, err := xmlquery.Parse(strings.NewReader(material))
			if err != nil {
				log.Warnf("parse xml failed: %s", err)
				return nil, utils.Errorf("xmlquery.Parse failed: %s", err)
			}
			for _, group := range y.Groups {
				if group != "" {
					nodes, err := xmlquery.QueryAll(doc, group)
					if err != nil {
						log.Errorf("xpath[%v] failed: %s", group, err)
						continue
					}
					for _, node := range nodes {
						addResult(node.InnerText())
					}
				}
			}
		} else {
			isXml = true
			doc, err := htmlquery.Parse(strings.NewReader(material))
			if err != nil {
				log.Warnf("parse html failed: %s", err)
				return nil, utils.Errorf("htmlquery.Parse failed: %s", err)
			}
			for _, group := range y.Groups {
				if group != "" {
					nodes, err := htmlquery.QueryAll(doc, group)
					if err != nil {
						log.Errorf("xpath[%v] failed: %s", group, err)
						continue
					}
					for _, node := range nodes {
						if y.XPathAttribute != "" {
							for _, attr := range node.Attr {
								if attr.Key == y.XPathAttribute {
									addResult(attr.Val)
									isXml = false
								}
							}
						} else {
							addResult(htmlquery.InnerText(node))
							isXml = false
						}
					}
				}
			}
			if isXml {
				goto TRYXML
			}
		}
	case "nuclei-dsl", "nuclei":
		box := NewNucleiDSLYakSandbox()
		header, body := lowhttp.SplitHTTPHeadersAndBodyFromPacket(rsp)
		for _, group := range y.Groups {
			if group != "" {
				data, err := box.Execute(group, map[string]interface{}{
					"body":     body,
					"header":   header,
					"raw":      string(rsp),
					"response": string(rsp),
				})
				if err != nil {
					continue
				}
				addResult(data)
			}
		}
	default:
		return nil, utils.Errorf("unknown extractor type: %s", t)
	}
	tag := y.Name
	if tag == "" {
		tag = "data"
	}
	return map[string]interface{}{tag: results}, nil
}

var (
	extractFromJSONValue = regexp.MustCompile(`("[^":]+")\s*?:\s*(("([^"\n])+")|([^"]\S+[^"\s,]))`)
	extractFromEqValue   = regexp.MustCompile(`(?i)([_a-z][^=\s]*)=(([^=\s&,"]+)|("[^"]+"))`)
)

func ExtractKValFromResponse(rsp []byte) map[string]interface{} {
	results := make(map[string]interface{})
	addResult := func(k, v string) {
		if _, ok := results[k]; !ok {
			results[k] = make([]interface{}, 0)
		}
		results[k] = append(results[k].([]interface{}), v)
	}
	_, body := lowhttp.SplitHTTPHeadersAndBodyFromPacketEx2(rsp, nil, func(proto string, code int, codeMsg string) error {
		addResult("proto", proto)
		addResult("status_code", fmt.Sprintf("%d", code))
		return nil
	}, func(line string) {
		k, v := lowhttp.SplitHTTPHeader(line)
		originKey := k
		k = strings.ReplaceAll(strings.ToLower(k), "-", "_")
		addResult(k, v)
		addResult(originKey, v)
		if k == `content_type` {
			ct, params, err := mime.ParseMediaType(v)
			if err != nil {
				return
			}
			addResult(`content_type`, ct)
			for k, v := range params {
				addResult(k, v)
			}
		} else {
			httphead.ScanOptions([]byte("__yaktpl_placeholder__; "+v+"; "), func(index int, option, attribute, value []byte) httphead.Control {
				decoded, err := url.QueryUnescape(string(value))
				if err != nil {
					addResult(string(attribute), string(value))
				} else {
					addResult(string(attribute), decoded)
				}
				return httphead.ControlContinue
			})
		}

	})
	// 特殊处理 JSON
	var skipJson = false
	for _, bodyRaw := range jsonextractor.ExtractStandardJSON(string(body)) {
		skipJson = true
		if govalidator.IsJSON(bodyRaw) {
			for i := 0; i < strings.Count(bodyRaw, "{")+2; i++ {
				for k := range jstream.NewDecoder(bytes.NewBufferString(bodyRaw), i).Stream() {
					switch k.ValueType {
					case jstream.Object:
						data := utils.InterfaceToMapInterface(k.Value)
						if data == nil {
							continue
						}
						for k, v := range data {
							switch v.(type) {
							case string, int64, float64, []int8, []byte, bool, float32:
								addResult(k, fmt.Sprintf("%v", v))
							default:
							}
						}
					}
				}
			}
		}
	}

	if !skipJson {
		for _, result := range extractFromJSONValue.FindAllStringSubmatch(string(body), -1) {
			if len(result) > 2 {
				key, value := strings.TrimSpace(result[1]), strings.TrimSpace(result[2])
				if strings.HasPrefix(key, `"`) && strings.HasSuffix(key, `"`) {
					key = key[1 : len(key)-1]
				}
				if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
					value = value[1 : len(value)-1]
				}
				addResult(key, value)
			}
		}
	}

	for _, result := range extractFromEqValue.FindAllStringSubmatch(string(body), -1) {
		if len(result) > 2 {
			key, value := strings.TrimSpace(result[1]), strings.TrimSpace(result[2])
			if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
				value = value[1 : len(value)-1]
			}
			addResult(key, value)
		}
	}

	return results
}
