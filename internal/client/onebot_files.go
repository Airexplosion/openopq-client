package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openopq/internal/mediaconv"
	"openopq/internal/protocol"
)

func (b *Bot) groupFileList(ctx context.Context, groupUin uint32, directory string) (protocol.GroupFileList, error) {
	var out protocol.GroupFileList
	var start uint32
	for page := 0; page < 40; page++ {
		body, cmd := protocol.BuildGroupFileList(groupUin, directory, start, 50)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return protocol.GroupFileList{}, err
		}
		part, err := protocol.ParseGroupFileList(answer)
		if err != nil {
			return protocol.GroupFileList{}, err
		}
		out.Files = append(out.Files, part.Files...)
		out.Folders = append(out.Folders, part.Folders...)
		if part.IsEnd || len(part.Files)+len(part.Folders) == 0 {
			out.IsEnd = true
			return out, nil
		}
		start += uint32(len(part.Files) + len(part.Folders))
	}
	return protocol.GroupFileList{}, fmt.Errorf("group file listing exceeded 40 pages; refusing to return a truncated listing")
}

func oneBotGroupFiles(group uint32, list protocol.GroupFileList) map[string]any {
	files := make([]map[string]any, 0, len(list.Files))
	for _, f := range list.Files {
		files = append(files, map[string]any{
			"group_id": group, "file_id": f.FileID, "file_name": f.FileName, "busid": f.BusID,
			"file_size": f.FileSize, "upload_time": f.UploadedTime, "dead_time": f.ExpireTime,
			"modify_time": f.ModifyTime, "download_times": f.DownloadTimes,
			"uploader": f.UploaderUin, "uploader_name": f.UploaderName,
		})
	}
	folders := make([]map[string]any, 0, len(list.Folders))
	for _, f := range list.Folders {
		folders = append(folders, map[string]any{
			"group_id": group, "folder_id": f.FolderID, "folder_name": f.FolderName,
			"create_time": f.CreateTime, "creator": f.CreatorUin, "creator_name": f.CreatorName,
			"total_file_count": f.TotalFileCount, "parent_id": f.ParentID,
		})
	}
	return map[string]any{"files": files, "folders": folders}
}

func (b *Bot) oneBotFileAction(ctx context.Context, action string, raw json.RawMessage) (any, error) {
	var p struct {
		GroupID         uint32 `json:"group_id"`
		FileID          string `json:"file_id"`
		FolderID        string `json:"folder_id"`
		Name            string `json:"name"`
		NewName         string `json:"new_name"`
		FolderName      string `json:"folder_name"`
		ParentDirectory string `json:"parent_directory"`
		TargetDirectory string `json:"target_directory"`
		FileHash        string `json:"file_hash"`
		BusID           uint32 `json:"busid"`
		Image           string `json:"image"`
		URL             string `json:"url"`
		Domain          string `json:"domain"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid action parameters: %w", err)
	}
	if p.FileID == "" {
		var alt struct {
			File string `json:"file"`
		}
		_ = json.Unmarshal(raw, &alt)
		p.FileID = alt.File
	}
	switch action {
	case "get_rkey", "nc_get_rkey":
		body, cmd := protocol.BuildFetchRKey()
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		keys, err := protocol.ParseFetchRKey(answer)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, map[string]any{
				"rkey": k.RKey, "ttl": k.TTL, "time": k.CreateTime, "type": k.Type,
			})
		}
		return map[string]any{"rkeys": out}, nil
	case "ocr_image", ".ocr_image":
		imageURL := p.Image
		if imageURL == "" {
			imageURL = p.URL
		}
		if imageURL == "" && p.FileID != "" {
			data, err := b.mediaAction(ctx, "get_image", raw)
			if err != nil {
				return nil, err
			}
			if m, ok := data.(map[string]any); ok {
				if u, _ := m["url"].(string); u != "" {
					imageURL = u
				} else if u, _ := m["file"].(string); strings.HasPrefix(u, "http") {
					imageURL = u
				}
			}
		}
		if imageURL == "" || len(imageURL) > 2048 || !strings.HasPrefix(imageURL, "http") {
			return nil, fmt.Errorf("image url required")
		}
		body, cmd := protocol.BuildImageOCR(imageURL)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		got, err := protocol.ParseImageOCR(answer)
		if err != nil {
			return nil, err
		}
		texts := make([]map[string]any, 0, len(got.Texts))
		for _, line := range got.Texts {
			coords := make([]map[string]any, 0, len(line.Coordinates))
			for _, c := range line.Coordinates {
				coords = append(coords, map[string]any{"x": c[0], "y": c[1]})
			}
			texts = append(texts, map[string]any{
				"text": line.Text, "confidence": line.Confidence, "coordinates": coords,
			})
		}
		return map[string]any{"texts": texts, "language": got.Language}, nil
	case "get_group_info_ex", "get_group_detail_info":
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		body, cmd := protocol.BuildFetchGroup(p.GroupID)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		g, err := protocol.ParseFetchGroup(answer)
		if err != nil {
			return nil, err
		}
		muted := 0
		if g.ShutupAll == ^uint32(0) || int64(g.ShutupAll) > time.Now().Unix() {
			muted = -1
		}
		return map[string]any{
			"group_id": g.GroupUin, "group_name": g.Name, "group_memo": g.Memo,
			"max_member_count": g.MaxMemberCount, "member_count": g.MemberCount,
			"group_create_time": g.CreateTime, "group_level": g.Level,
			"group_all_shut": muted, "group_owner": g.OwnerUin(),
			"owner_uid": g.Owner, "last_msg_time": g.LastMsgTime, "cur_msg_seq": g.CurMsgSeq,
			"question": g.Question, "answer": g.Answer,
		}, nil
	case "get_group_root_files":
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		list, err := b.groupFileList(ctx, p.GroupID, "/")
		if err != nil {
			return nil, err
		}
		return oneBotGroupFiles(p.GroupID, list), nil
	case "get_group_files_by_folder":
		if p.GroupID == 0 || p.FolderID == "" {
			return nil, fmt.Errorf("group_id and folder_id required")
		}
		list, err := b.groupFileList(ctx, p.GroupID, p.FolderID)
		if err != nil {
			return nil, err
		}
		return oneBotGroupFiles(p.GroupID, list), nil
	case "get_group_file_system_info":
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		countBody, countCmd := protocol.BuildGroupFileCount(p.GroupID)
		countAnswer, err := b.oidbAnswer(ctx, countBody, countCmd)
		if err != nil {
			return nil, err
		}
		fileCount, limit, err := protocol.ParseGroupFileCount(countAnswer)
		if err != nil {
			return nil, err
		}
		spaceBody, spaceCmd := protocol.BuildGroupFileSpace(p.GroupID)
		spaceAnswer, err := b.oidbAnswer(ctx, spaceBody, spaceCmd)
		if err != nil {
			return nil, err
		}
		total, used, err := protocol.ParseGroupFileSpace(spaceAnswer)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"file_count": fileCount, "limit_count": limit,
			"used_space": used, "total_space": total,
		}, nil
	case "get_group_file_url":
		if p.GroupID == 0 || p.FileID == "" {
			return nil, fmt.Errorf("group_id and file_id required")
		}
		body, cmd := protocol.BuildGroupFileDownload(p.GroupID, p.FileID, p.BusID)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		u, err := protocol.ParseGroupFileDownload(answer)
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": u}, nil
	case "delete_group_file":
		if p.GroupID == 0 || p.FileID == "" {
			return nil, fmt.Errorf("group_id and file_id required")
		}
		body, cmd := protocol.BuildGroupFileDelete(p.GroupID, p.FileID, p.BusID)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		return nil, protocol.ParseGroupFileDelete(answer, 4)
	case "create_group_file_folder":
		if p.GroupID == 0 || p.Name == "" {
			return nil, fmt.Errorf("group_id and name required")
		}
		if err := rejectFSName(p.Name); err != nil {
			return nil, err
		}
		parent := p.FolderID
		if parent == "" {
			parent = "/"
		}
		body, cmd := protocol.BuildGroupFolderCreate(p.GroupID, parent, p.Name)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		folder, err := protocol.ParseGroupFolderCreate(answer)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"folder_id": folder.FolderID, "folder_name": folder.FolderName,
			"create_time": folder.CreateTime, "creator": folder.CreatorUin,
		}, nil
	case "delete_group_folder":
		if p.GroupID == 0 || p.FolderID == "" {
			return nil, fmt.Errorf("group_id and folder_id required")
		}
		body, cmd := protocol.BuildGroupFolderDelete(p.GroupID, p.FolderID)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		return nil, protocol.ParseGroupFileDelete(answer, 2)
	case "move_group_file":
		target := p.FolderID
		if target == "" {
			target = p.TargetDirectory
		}
		if p.GroupID == 0 || p.FileID == "" || target == "" {
			return nil, fmt.Errorf("group_id, file_id and folder_id required")
		}
		if err := rejectFSToken(p.FileID, 256); err != nil {
			return nil, err
		}
		body, cmd := protocol.BuildGroupFileMove(p.GroupID, p.FileID, p.ParentDirectory, target, p.BusID)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		return nil, protocol.ParseGroupFileDelete(answer, 6)
	case "rename_group_file":
		name := firstNonEmpty(p.Name, p.NewName)
		if p.GroupID == 0 || p.FileID == "" || name == "" {
			return nil, fmt.Errorf("group_id, file_id and name required")
		}
		if err := rejectFSToken(p.FileID, 256); err != nil {
			return nil, err
		}
		if err := rejectFSName(name); err != nil {
			return nil, err
		}
		body, cmd := protocol.BuildGroupFileRename(p.GroupID, p.FileID, p.ParentDirectory, name, p.BusID)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		return nil, protocol.ParseGroupFileDelete(answer, 5)
	case "set_group_file_folder_name", "rename_group_file_folder":
		name := firstNonEmpty(p.Name, p.FolderName, p.NewName)
		if p.GroupID == 0 || p.FolderID == "" || name == "" {
			return nil, fmt.Errorf("group_id, folder_id and name required")
		}
		if err := rejectFSName(name); err != nil {
			return nil, err
		}
		body, cmd := protocol.BuildGroupFolderRename(p.GroupID, p.FolderID, name)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		return nil, protocol.ParseGroupFileDelete(answer, 3)
	case "trans_group_file":
		if p.GroupID == 0 || p.FileID == "" {
			return nil, fmt.Errorf("group_id and file_id required")
		}
		if err := rejectFSToken(p.FileID, 256); err != nil {
			return nil, err
		}
		body, cmd := protocol.BuildGroupSendFile(p.GroupID, p.FileID, randU32())
		return nil, b.oidbDo(ctx, body, cmd)
	case "get_private_file_url":
		if p.FileID == "" || p.FileHash == "" {
			return nil, fmt.Errorf("file_id and file_hash required")
		}
		if err := rejectFSToken(p.FileID, 256); err != nil {
			return nil, err
		}
		if err := rejectFSToken(p.FileHash, 256); err != nil {
			return nil, err
		}
		uid := b.Credentials().Uid
		if uid == "" {
			return nil, fmt.Errorf("account UID is unknown")
		}
		body, cmd := protocol.BuildPrivateFileDownload(uid, p.FileID, p.FileHash)
		answer, err := b.oidbAnswer(ctx, body, cmd)
		if err != nil {
			return nil, err
		}
		u, err := protocol.ParsePrivateFileDownload(answer)
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": u}, nil
	case "download_file":
		return b.downloadNamedFile(ctx, raw)
	default:
		return nil, fmt.Errorf("unsupported file action")
	}
}

func (b *Bot) downloadNamedFile(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		URL     string `json:"url"`
		Name    string `json:"name"`
		Headers string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.URL == "" {
		return nil, fmt.Errorf("url required")
	}
	if len(p.URL) > 2048 || strings.ContainsAny(p.URL, "\x00\r\n") {
		return nil, fmt.Errorf("invalid url")
	}
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, fmt.Errorf("invalid url")
	}
	if err := rejectMetadataDownloadHost(u.Hostname()); err != nil {
		return nil, err
	}
	r := &b.obMedia
	r.op.Lock()
	defer r.op.Unlock()
	if r.root == "" {
		r.root, err = mediaconv.TempDir("account-")
		if err != nil {
			return nil, err
		}
	}
	remaining, err := b.mediaCacheRemaining()
	if err != nil {
		return nil, err
	}
	limit := min(int64(30<<20), remaining)
	name := filepath.Base(p.Name)
	if name == "" || name == "." || name == ".." {
		name = filepath.Base(u.Path)
	}
	if name == "" || name == "." || name == "/" {
		name = "download"
	}
	path := filepath.Join(r.root, strconv.FormatInt(time.Now().UnixNano(), 10)+"-"+name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(p.Headers, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		req.Header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	transport := newDownloadTransport(net.DefaultResolver.LookupIPAddr)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
			return fmt.Errorf("invalid redirect")
		}
		return rejectMetadataDownloadHost(req.URL.Hostname())
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return nil, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return nil, closeErr
	}
	if n > limit {
		_ = os.Remove(path)
		return nil, fmt.Errorf("download exceeds 30 MiB or remaining account cache space; use clean_cache or increase MediaCacheMiB")
	}
	return map[string]any{"file": path, "file_name": name, "file_size": n}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func rejectFSName(name string) error {
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("invalid name")
	}
	return nil
}

func rejectFSToken(v string, max int) error {
	if v == "" || len(v) > max || strings.ContainsAny(v, "\x00\r\n") {
		return fmt.Errorf("invalid file identifier")
	}
	return nil
}

func rejectMetadataDownloadHost(host string) error {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "metadata.google.internal" {
		return fmt.Errorf("download host is not allowed")
	}
	ip := net.ParseIP(strings.Split(host, "%")[0])
	if ip == nil {
		return nil
	}
	if ip.IsLinkLocalUnicast() || ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return fmt.Errorf("download host is not allowed")
	}
	return nil
}

// Resolve and validate at dial time, then dial the checked numeric address so
// DNS cannot change between validation and connection. Redirects use this same
// transport. Do not use environment proxies: their DNS resolution is outside
// this process's control and could bypass the IP checks.
func newDownloadTransport(lookup func(context.Context, string) ([]net.IPAddr, error)) *http.Transport {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if err = rejectMetadataDownloadHost(host); err != nil {
				return nil, err
			}
			ips, err := lookup(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("download host has no IP addresses")
			}
			// Reject the entire answer if any address is forbidden, regardless
			// of DNS ordering or which address would connect first.
			for _, ip := range ips {
				if ip.IP == nil {
					return nil, fmt.Errorf("invalid download IP address")
				}
				if err := rejectMetadataDownloadHost(ip.String()); err != nil {
					return nil, err
				}
			}
			for _, ip := range ips {
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				err = dialErr
			}
			return nil, err
		},
	}
}
