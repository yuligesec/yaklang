package yakgrpc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yaklang/yaklang/common/consts"
	"github.com/yaklang/yaklang/common/filter"
	"github.com/yaklang/yaklang/common/log"
	"github.com/yaklang/yaklang/common/utils"
	"github.com/yaklang/yaklang/common/utils/bizhelper"
	"github.com/yaklang/yaklang/common/yakgrpc/yakit"
	"github.com/yaklang/yaklang/common/yakgrpc/ypb"
)

func grpc2Paging(pag *ypb.Paging) *yakit.Paging {
	ret := yakit.NewPaging()
	if pag != nil {
		ret.Order = pag.GetOrder()
		ret.OrderBy = pag.GetOrderBy()
		ret.Page = int(pag.GetPage())
		ret.Limit = int(pag.GetLimit())
	}
	return ret
}

func Payload2Grpc(r *yakit.Payload) *ypb.Payload {
	raw, err := strconv.Unquote(*r.Content)
	if err != nil {
		raw = *r.Content
	}
	p := &ypb.Payload{
		Id:           int64(r.ID),
		Group:        r.Group,
		ContentBytes: []byte(raw),
		Content:      utils.EscapeInvalidUTF8Byte([]byte(raw)),
		// Folder:       *r.Folder,
		// HitCount:     *r.HitCount,
		// IsFile:       *r.IsFile,
	}
	if r.Folder != nil {
		p.Folder = *r.Folder
	}
	if r.HitCount != nil {
		p.HitCount = *r.HitCount
	}
	if r.IsFile != nil {
		p.IsFile = *r.IsFile
	}
	return p
}
func grpc2Payload(p *ypb.Payload) *yakit.Payload {
	payload := &yakit.Payload{
		Group:    p.Group,
		Content:  &p.Content,
		Folder:   &p.Folder,
		HitCount: &p.HitCount,
		IsFile:   &p.IsFile,
	}
	payload.Hash = payload.CalcHash()
	return payload
}

func (s *Server) QueryPayload(ctx context.Context, req *ypb.QueryPayloadRequest) (*ypb.QueryPayloadResponse, error) {
	if req == nil {
		return nil, utils.Errorf("empty parameter")
	}
	p, d, err := yakit.QueryPayload(s.GetProfileDatabase(), req.GetFolder(), req.GetGroup(), req.GetKeyword(), grpc2Paging(req.GetPagination()))
	if err != nil {
		return nil, err
	}

	var items []*ypb.Payload
	for _, r := range d {
		items = append(items, Payload2Grpc(r))
	}

	return &ypb.QueryPayloadResponse{
		Pagination: req.Pagination,
		Total:      int64(p.TotalRecord),
		Data:       items,
	}, nil
}

const (
	FiveMB = 5 * 1024 * 1024 // 5 MB in bytes
	OneKB  = 1 * 1024
)

func (s *Server) QueryPayloadFromFile(ctx context.Context, req *ypb.QueryPayloadFromFileRequest) (*ypb.QueryPayloadFromFileResponse, error) {
	if req.GetGroup() == "" {
		return nil, utils.Error("group name is empty")
	}
	filename, err := yakit.GetPayloadGroupFileName(s.GetProfileDatabase(), req.GetGroup())
	if err != nil {
		return nil, err
	}
	var size int64
	{
		if state, err := os.Stat(filename); err != nil {
			return nil, err
		} else {
			size += state.Size()
		}
	}

	f, err := os.Open(filename)
	if err != nil {
		return nil, utils.Errorf("failed to read file: %s", err)
	}

	reader := bufio.NewReader(f)
	outC := make(chan []byte)
	done := make(chan bool)
	go func() {
		defer f.Close()
		defer close(outC)
		for {
			select {
			case <-done:
				return
			default:
				lineRaw, err := utils.BufioReadLine(reader)
				if err != nil {
					return
				}
				raw := bytes.TrimSpace(lineRaw)
				outC <- raw
			}
		}
	}()
	var handlerSize int64 = 0

	defer close(done)

	data := make([]byte, 0, size)
	for line := range outC {
		handlerSize += int64(len(line))
		if s, err := strconv.Unquote(string(line)); err == nil {
			line = []byte(s)
		}
		line = append(line, "\r\n"...)
		data = append(data, line...)
		if size > FiveMB && handlerSize > OneKB {
			// If file is larger than 5MB, read only the first 50KB
			return &ypb.QueryPayloadFromFileResponse{
				Data:      data,
				IsBigFile: true,
			}, nil
		}
	}

	return &ypb.QueryPayloadFromFileResponse{
		Data:      data,
		IsBigFile: false,
	}, nil
}

func (s *Server) DeletePayloadByFolder(ctx context.Context, req *ypb.NameRequest) (*ypb.Empty, error) {
	if req.GetName() == "" {
		return nil, utils.Errorf("folder name is empty ")
	}
	if err := yakit.DeletePayloadByFolder(s.GetProfileDatabase(), req.GetName()); err != nil {
		return nil, err
	}
	return &ypb.Empty{}, nil
}

func (s *Server) DeletePayloadByGroup(ctx context.Context, req *ypb.DeletePayloadByGroupRequest) (*ypb.Empty, error) {
	if req.GetGroup() == "" {
		return nil, utils.Errorf("group name is empty ")
	}
	// if file, delete  file
	if group, err := yakit.GetPayloadByGroupFirst(s.GetProfileDatabase(), req.GetGroup()); err != nil {
		return nil, err
	} else {
		if group.IsFile != nil && *group.IsFile {
			// delete file
			if err := os.Remove(*group.Content); err != nil {
				return nil, err
			}
		}
	}
	// delete in database
	if err := yakit.DeletePayloadByGroup(s.GetProfileDatabase(), req.GetGroup()); err != nil {
		return nil, err
	}
	return &ypb.Empty{}, nil
}

func (s *Server) DeletePayload(ctx context.Context, req *ypb.DeletePayloadRequest) (*ypb.Empty, error) {
	if req.GetId() > 0 {
		if err := yakit.DeletePayloadByID(s.GetProfileDatabase(), req.GetId()); err != nil {
			return nil, utils.Wrap(err, "delete single line failed")
		}
	}

	if len(req.GetIds()) > 0 {
		if err := yakit.DeletePayloadByIDs(s.GetProfileDatabase(), req.GetIds()); err != nil {
			return nil, utils.Wrap(err, "delete multi line failed")
		}
	}

	return &ypb.Empty{}, nil
}

const (
	OneMB = 1 * 1024 * 1024 // 5 MB in bytes
)

func (s *Server) SavePayloadStream(req *ypb.SavePayloadRequest, stream ypb.Yak_SavePayloadStreamServer) (ret error) {
	if (!req.IsFile && req.Content == "") || (req.IsFile && len(req.FileName) == 0) || (req.Group == "") {
		return utils.Error("content or file name or Group is empty ")
	}

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	var size, total int64
	_ = size
	start := time.Now()
	feedback := func(progress float64, msg string) {
		if progress == -1 {
			progress = float64(size) / float64(total)
		}
		d := time.Since(start)
		speed := float64((size)/OneMB) / (d.Seconds())
		rest := float64((total-size)/OneMB) / (speed)
		stream.Send(&ypb.SavePayloadProgress{
			Progress:            progress,
			Speed:               fmt.Sprintf("%.2f", speed),
			CostDurationVerbose: fmt.Sprintf("%.2fs", d.Seconds()),
			RestDurationVerbose: fmt.Sprintf("%.2f", rest),
			Message:             msg,
		})
	}
	// _ = feedback
	go func() {
		defer func() {
			size = total
		}()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(500 * time.Millisecond)
				feedback(float64(size)/float64(total), "")
			}
		}
	}()

	feedback(0, "start")
	handleFile := func(f string) error {
		if state, err := os.Stat(f); err != nil {
			return err
		} else {
			total += state.Size()
		}
		defer feedback(-1, "文件 "+f+" 写入数据库成功")
		feedback(-1, "正在读取文件: "+f)
		return yakit.SavePayloadByFilenameEx(f, func(data string, hitCount int64) error {
			size += int64(len(data))
			return yakit.CreateAndUpdatePayload(s.GetProfileDatabase(), data, req.GetGroup(), req.GetFolder(), hitCount)
		})
	}

	defer func() {
		if total == 0 {
			ret = utils.Error("empty data no payload created")
		} else {
			feedback(1, "数据保存成功")
			yakit.SetGroupInEnd(s.GetProfileDatabase(), req.GetGroup())
		}
	}()
	if req.IsFile {
		for _, f := range req.FileName {
			err := handleFile(f)
			if err != nil {
				log.Errorf("handle file %s error: %s", f, err.Error())
				continue
			}
		}
	} else {
		total = int64(len(req.GetContent()))
		feedback(-1, "正在读取数据 ")
		if err := yakit.SavePayloadGroupByRawEx(req.GetContent(), func(data string) error {
			size += int64(len(data))
			return yakit.CreateAndUpdatePayload(s.GetProfileDatabase(), data, req.GetGroup(), req.GetFolder(), 0)
		}); err != nil {
			log.Errorf("save payload group by content error: %s", err.Error())
		}
	}
	return nil
}

func (s *Server) SavePayloadToFileStream(req *ypb.SavePayloadRequest, stream ypb.Yak_SavePayloadToFileStreamServer) error {
	if (!req.IsFile && req.Content == "") || (req.IsFile && len(req.FileName) == 0) || (req.Group == "") {
		return utils.Error("content and file name all is empty ")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	var handledSize, filtered, duplicate, total int64
	feedback := func(progress float64, msg string) {
		if progress == -1 {
			progress = float64(handledSize) / float64(total)
		}
		stream.Send(&ypb.SavePayloadProgress{
			Progress: progress,
			Message:  msg,
		})
	}

	data := make([]struct {
		data     string
		hitCount int64
	}, 0)
	filter := filter.NewFilter()
	saveDataByFilter := func(s string, hitCount int64) error {
		handledSize += int64(len(s))
		if !filter.Exist(s) {
			filtered++
			filter.Insert(s)
			data = append(data,
				struct {
					data     string
					hitCount int64
				}{
					s, hitCount,
				})
		} else {
			duplicate++
		}
		return nil
	}
	saveDataByFilterNoHitCount := func(s string) error {
		return saveDataByFilter(s, 0)
	}

	handleFile := func(f string) error {
		if state, err := os.Stat(f); err != nil {
			return err
		} else {
			total += state.Size()
		}
		feedback(-1, "正在读取文件: "+f)
		return yakit.SavePayloadByFilenameEx(f, saveDataByFilter)
	}

	if req.IsFile {
		feedback(0, "开始解析文件")
		for _, file := range req.FileName {
			if err := handleFile(file); err != nil {
				log.Errorf("open file %s error: %s", file, err.Error())
			}
		}
	} else {
		total += int64(len(req.GetContent()))
		feedback(0, "开始解析数据")
		yakit.SavePayloadGroupByRawEx(req.GetContent(), saveDataByFilterNoHitCount)
	}

	feedback(1, fmt.Sprintf("检测到有%d项重复数据", duplicate))
	feedback(1, fmt.Sprintf("已去除重复数据, 剩余%d项数据", filtered))

	feedback(1, "step2")
	start := time.Now()
	feedback = func(progress float64, msg string) {
		if progress == 0 {
			progress = float64(handledSize) / float64(total)
		}
		d := time.Since(start)
		speed := float64((handledSize)/OneMB) / (d.Seconds())
		rest := float64((total-handledSize)/OneMB) / (speed)
		stream.Send(&ypb.SavePayloadProgress{
			Progress:            progress,
			Speed:               fmt.Sprintf("%f", speed),
			CostDurationVerbose: d.String(),
			RestDurationVerbose: fmt.Sprintf("%f", rest),
			Message:             msg,
		})
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(time.Second)
				feedback(-1, "")
			}
		}
	}()
	handledSize = 0
	total = int64(len(data))
	// save to file
	ProjectFolder := consts.GetDefaultYakitPayloadsDir()
	fileName := fmt.Sprintf("%s/%s_%s.txt", ProjectFolder, req.GetFolder(), req.GetGroup())
	fd, err := os.OpenFile(fileName, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	feedback(0, "正在写入文件")
	for i, d := range data {
		handledSize = int64(i)
		if i == int(total)-1 {
			fd.WriteString(d.data)
		} else {
			fd.WriteString(d.data + "\r\n")
		}
	}
	if err := fd.Close(); err != nil {
		return err
	}
	feedback(0.99, "写入文件完成")
	folder := req.GetFolder()
	f := true
	payload := yakit.NewPayload(req.GetGroup(), fileName)
	payload.Folder = &folder
	payload.IsFile = &f
	yakit.CreateOrUpdatePayload(s.GetProfileDatabase(), payload)
	yakit.SetGroupInEnd(s.GetProfileDatabase(), req.GetGroup())
	if total == 0 {
		return utils.Error("empty data no payload created")
	}
	feedback(1, "导入完成")
	return nil
}

func (s *Server) RenamePayloadFolder(ctx context.Context, req *ypb.RenameRequest) (*ypb.Empty, error) {
	if req.GetName() == "" || req.GetNewName() == "" {
		return nil, utils.Error("old folder or folder can't be empty")
	}
	if err := yakit.RenamePayloadGroup(s.GetProfileDatabase(), getEmptyFolderName(req.GetName()), getEmptyFolderName(req.GetNewName())); err != nil {
		return nil, err
	}
	if err := yakit.RenamePayloadFolder(s.GetProfileDatabase(), req.GetName(), req.GetNewName()); err != nil {
		return nil, err
	} else {
		return &ypb.Empty{}, nil
	}
}

func (s *Server) RenamePayloadGroup(ctx context.Context, req *ypb.RenameRequest) (*ypb.Empty, error) {
	if req.GetName() == "" || req.GetNewName() == "" {
		return nil, utils.Error("group name and new name can't be empty")
	}

	if err := yakit.RenamePayloadGroup(s.GetProfileDatabase(), req.GetName(), req.GetNewName()); err != nil {
		return nil, err
	} else {
		return &ypb.Empty{}, nil
	}
}

func (s *Server) UpdatePayload(ctx context.Context, req *ypb.UpdatePayloadRequest) (*ypb.Empty, error) {
	// just for old version
	if req.Group != "" || req.OldGroup != "" {
		yakit.RenamePayloadGroup(s.GetProfileDatabase(), req.OldGroup, req.Group)
		return &ypb.Empty{}, nil
	}

	if req.GetId() == 0 || req.GetData() == nil {
		return nil, utils.Error("id or data can't be empty")
	}
	if err := yakit.UpdatePayload(s.GetProfileDatabase(), int(req.GetId()), grpc2Payload(req.GetData())); err != nil {
		return nil, err
	} else {
		return &ypb.Empty{}, nil
	}
}

func writeDataToFileEnd(filename, data string, flag int) error {
	// Open the file in append mode.
	// If the file doesn't exist, create it.
	file, err := os.OpenFile(filename, flag, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Write the data to the file.
	_, err = file.WriteString(data)
	if err != nil {
		return err
	}
	return nil
}

// rpc RemoveDuplicatePayloads(RemoveDuplicatePayloadsRequest) returns (stream SavePayloadProgress);
func (s *Server) RemoveDuplicatePayloads(req *ypb.NameRequest, stream ypb.Yak_RemoveDuplicatePayloadsServer) error {
	if req.GetName() == "" {
		return utils.Error("group can't be empty")
	}
	filename, err := yakit.GetPayloadGroupFileName(s.GetProfileDatabase(), req.GetName())
	if err != nil {
		return utils.Wrapf(err, "this group not a file payload group")
	}

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	var handledSize, filtered, duplicate, total int64
	if state, err := os.Stat(filename); err != nil {
		return err
	} else {
		total += state.Size()
	}
	total += 1
	feedback := func(progress float64, msg string) {
		if progress == -1 {
			progress = float64(handledSize) / float64(total)
		}
		stream.Send(&ypb.SavePayloadProgress{
			Progress: progress,
			Message:  msg,
		})
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(time.Second)
				feedback(-1, "")
			}
		}
	}()
	outC, err := utils.FileLineReader(filename)
	if err != nil {
		return err
	}

	data := make([]string, 0)
	filter := filter.NewFilter()

	for lineB := range outC {
		line := utils.UnsafeBytesToString(lineB)
		handledSize += int64(len(line))
		if !filter.Exist(line) {
			filtered++
			filter.Insert(line)
			data = append(data, line)
		} else {
			duplicate++
		}
	}

	feedback(0, "正在读取数据")
	feedback(0.99, fmt.Sprintf("检测到有%d项重复数据", duplicate))
	feedback(0.99, fmt.Sprintf("已去除重复数据, 剩余%d项数据", filtered))
	feedback(0.99, "正在保存到文件")
	defer feedback(1, "保存成功")
	if err := writeDataToFileEnd(filename, strings.Join(data, "\r\n"), os.O_WRONLY|os.O_TRUNC); err != nil {
		return err
	} else {
		return nil
	}
}

func (s *Server) UpdatePayloadToFile(ctx context.Context, req *ypb.UpdatePayloadToFileRequest) (*ypb.Empty, error) {
	if req.GetGroupName() == "" {
		return nil, utils.Error("group can't be empty")
	}
	if filename, err := yakit.GetPayloadGroupFileName(s.GetProfileDatabase(), req.GetGroupName()); err != nil {
		return nil, err
	} else {
		data := make([]string, 0)
		yakit.SavePayloadGroupByRawEx(req.GetContent(), func(s string) error {
			data = append(data, s)
			return nil
		})
		if err := writeDataToFileEnd(filename, strings.Join(data, "\r\n"), os.O_WRONLY|os.O_TRUNC); err != nil {
			return nil, err
		} else {
			return &ypb.Empty{}, nil
		}
	}
}

func (s *Server) BackUpOrCopyPayloads(ctx context.Context, req *ypb.BackUpOrCopyPayloadsRequest) (*ypb.Empty, error) {
	if len(req.GetIds()) == 0 || req.GetGroup() == "" {
		return nil, utils.Error("id or group name can't be empty")
	}

	if groupFirstPayload, err := yakit.GetPayloadByGroupFirst(s.GetProfileDatabase(), req.GetGroup()); err != nil {
		return nil, err
	} else if groupFirstPayload.IsFile != nil && *groupFirstPayload.IsFile {
		db := s.GetProfileDatabase().Model(&yakit.Payload{})
		db = bizhelper.ExactQueryInt64ArrayOr(db, "id", req.GetIds())
		var payloads []yakit.Payload
		if err := db.Find(&payloads).Error; err != nil {
			return nil, utils.Wrap(err, "error finding payloads")
		}

		for _, payload := range payloads {
			// write to target file payload group
			if err := writeDataToFileEnd(*groupFirstPayload.Content, *payload.Content, os.O_WRONLY); err != nil {
				return nil, err
			} else {
				return &ypb.Empty{}, nil
			}
		}
		if !req.Copy {
			// if move to target
			// just delete original payload
			yakit.DeleteDomainByID(s.GetProfileDatabase(), req.GetIds()...)
		}
	} else {
		if req.Copy {
			// copy payloads to database
			yakit.CopyPayloads(s.GetProfileDatabase(), req.GetIds(), req.GetGroup(), req.GetFolder())
		} else {
			// move payloads to database
			yakit.MovePayloads(s.GetProfileDatabase(), req.GetIds(), req.GetGroup(), req.GetFolder())
		}
	}
	return &ypb.Empty{}, nil
}

func getEmptyFolderName(folder string) string {
	return folder + "///empty"
}

func (s *Server) CreatePayloadFolder(ctx context.Context, req *ypb.NameRequest) (*ypb.Empty, error) {
	if req.Name == "" {
		return nil, utils.Errorf("name is Empty")
	}
	if err := yakit.CreateAndUpdatePayload(s.GetProfileDatabase(), "", getEmptyFolderName(req.Name), req.Name, 0); err != nil {
		return nil, err
	} else {
		return &ypb.Empty{}, nil
	}
}

func (s *Server) UpdateAllPayloadGroup(ctx context.Context, req *ypb.UpdateAllPayloadGroupRequest) (*ypb.Empty, error) {
	nodes := req.Nodes
	folder := ""
	var index int64 = 0
	for _, node := range nodes {
		if node.Type == "Folder" {
			yakit.SetIndexToFolder(s.GetProfileDatabase(), node.Name, getEmptyFolderName(node.Name), index)
			folder = node.Name
			for _, child := range node.Nodes {
				yakit.UpdatePayloadGroup(s.GetProfileDatabase(), child.Name, folder, index)
				index++
			}
			folder = ""
		} else {
			yakit.UpdatePayloadGroup(s.GetProfileDatabase(), node.Name, folder, index)
		}
		index++
	}
	return &ypb.Empty{}, nil
}

func (s *Server) GetAllPayloadGroup(ctx context.Context, _ *ypb.Empty) (*ypb.GetAllPayloadGroupResponse, error) {
	type result struct {
		Group    string
		NumGroup int64
		Folder   *string
		IsFile   *bool
	}

	var res []result

	rows, err := s.GetProfileDatabase().Table("payloads").Select(`"group", COUNT("group") as num_group, folder, is_file`).Group(`"group"`).Order("group_index asc").Rows()
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		var r result
		if err := rows.Scan(&r.Group, &r.NumGroup, &r.Folder, &r.IsFile); err != nil {
			return nil, err
		}
		res = append(res, r)
	}

	groups := make([]string, 0, len(res))
	nodes := make([]*ypb.PayloadGroupNode, 0)
	folders := make(map[string]*ypb.PayloadGroupNode)
	add2Folder := func(folder string, node *ypb.PayloadGroupNode) (ret *ypb.PayloadGroupNode) {
		// skip group="" payload, this is empty folder
		folderNode, ok := folders[folder]
		if !ok {
			folderNode = &ypb.PayloadGroupNode{
				Type:   "Folder",
				Name:   folder,
				Number: 0,
				Nodes:  make([]*ypb.PayloadGroupNode, 0),
			}
			folders[folder] = folderNode
			ret = folderNode
		}
		if node.Name != getEmptyFolderName(folder) {
			folderNode.Nodes = append(folderNode.Nodes, node)
			folderNode.Number += node.Number
		}
		return
	}
	for _, r := range res {
		if r.Folder != nil && r.Group != getEmptyFolderName(*r.Folder) {
			groups = append(groups, r.Group)
		}
		typ := "DataBase"
		if r.IsFile != nil && *r.IsFile {
			typ = "File"
		}

		node := &ypb.PayloadGroupNode{
			Type:   typ,
			Name:   r.Group,
			Number: r.NumGroup,
			Nodes:  nil,
		}
		if r.Folder != nil && *r.Folder != "" {
			if n := add2Folder(*r.Folder, node); n != nil {
				nodes = append(nodes, n)
			}
		} else {
			nodes = append(nodes, node)
		}
	}

	return &ypb.GetAllPayloadGroupResponse{
		Groups: groups,
		Nodes:  nodes,
	}, nil
}

func (s *Server) GetAllPayload(ctx context.Context, req *ypb.GetAllPayloadRequest) (*ypb.GetAllPayloadResponse, error) {
	if req.GetGroup() == "" {
		return nil, utils.Errorf("group is empty")
	}
	db := bizhelper.ExactQueryString(s.GetProfileDatabase(), "`group`", req.GetGroup())
	db = bizhelper.ExactQueryString(db, "`folder`", req.GetFolder())

	var payloads []*ypb.Payload
	gen := yakit.YieldPayloads(db, context.Background())

	for p := range gen {
		payloads = append(payloads, Payload2Grpc(p))
	}

	return &ypb.GetAllPayloadResponse{
		Data: payloads,
	}, nil
}

func (s *Server) GetAllPayloadFromFile(req *ypb.GetAllPayloadRequest, stream ypb.Yak_GetAllPayloadFromFileServer) error {
	if req.GetGroup() == "" {
		return utils.Errorf("group is empty")
	}
	if filename, err := yakit.GetPayloadGroupFileName(s.GetProfileDatabase(), req.GetGroup()); err != nil {
		return err
	} else {

		var size, total int64
		if state, err := os.Stat(filename); err != nil {
			return err
		} else {
			total += state.Size()
		}

		ch, err := utils.FileLineReader(filename)
		if err != nil {
			return utils.Wrap(err, "read file error")
		}
		defer stream.Send(&ypb.GetAllPayloadFromFileResponse{
			Progress: 1,
			Data:     []byte{},
		})

		for lineB := range ch {
			line := utils.UnsafeBytesToString(lineB)
			size += int64(len(line)) + 1
			if s, err := strconv.Unquote(line); err == nil {
				line = s
			}
			stream.Send(&ypb.GetAllPayloadFromFileResponse{
				Progress: float64(size) / float64(total),
				Data:     utils.UnsafeStringToBytes(line),
			})
		}
		return nil
	}
}

func (s *Server) CoverPayloadGroupToDatabase(req *ypb.NameRequest, stream ypb.Yak_CoverPayloadGroupToDatabaseServer) error {
	if req.GetName() == "" {
		return utils.Errorf("group is empty")
	}

	group, err := yakit.GetPayloadByGroupFirst(s.GetProfileDatabase(), req.GetName())
	if err != nil {
		return err
	}
	if group.IsFile == nil && !*group.IsFile {
		return utils.Errorf("group is not file")
	}

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	var size, total int64
	_ = size
	start := time.Now()
	feedback := func(progress float64, msg string) {
		if progress == -1 {
			progress = float64(size) / float64(total)
		}
		d := time.Since(start)
		speed := float64((size)/OneMB) / (d.Seconds())
		rest := float64((total-size)/OneMB) / (speed)
		stream.Send(&ypb.SavePayloadProgress{
			Progress:            progress,
			Speed:               fmt.Sprintf("%.2f", speed),
			CostDurationVerbose: fmt.Sprintf("%.2fs", d.Seconds()),
			RestDurationVerbose: fmt.Sprintf("%.2f", rest),
			Message:             msg,
		})
	}
	// _ = feedback
	go func() {
		defer func() {
			size = total
		}()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(500 * time.Millisecond)
				feedback(-1, "")
			}
		}
	}()

	feedback(0, "start")
	if err := yakit.DeletePayloadByID(s.GetProfileDatabase(), int64(group.ID)); err != nil {
		return err
	}
	if group.Content == nil || *group.Content == "" {
		return utils.Error("this group filename  is empty")
	}
	folder := ""
	if group.Folder != nil {
		folder = *group.Folder
	} else {
		utils.Error("this folder is nil, please try agin.")
	}
	var groupindex int64 = 0
	if group.GroupIndex != nil {
		groupindex = *group.GroupIndex
	} else {
		return utils.Error("this group index is empty, please try again.")
	}

	filename := *group.Content
	if state, err := os.Stat(filename); err != nil {
		return err
	} else {
		total += state.Size()
	}
	feedback(-1, "正在读取文件: "+filename)
	defer func() {
		feedback(1, "转换完成, 该Payload字典已经转换为数据库存储。")
		os.Remove(filename)
	}()

	ch, err := utils.FileLineReader(filename)
	if err != nil {
		return err
	}

	for lineB := range ch {
		line := utils.UnsafeBytesToString(lineB)
		size += int64(len(line))
		yakit.CreateAndUpdatePayload(s.GetProfileDatabase(), line, group.Group, folder, 0)
	}
	yakit.UpdatePayloadGroup(s.GetProfileDatabase(), group.Group, folder, groupindex)
	return nil
}
