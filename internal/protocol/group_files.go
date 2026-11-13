package protocol

import (
	"encoding/hex"
	"fmt"
)

// Group file system packets are Oidb 0x6d6 / 0x6d7 / 0x6d8, copied from
// LagrangeGo client/packets/oidb/group_file_*.go. Those builders address
// the requests by UID (reserved=1).

const groupFSAppID = 7
const groupFSBusID = 102

// BuildGroupFileList asks 0x6d8_1 for one page of a directory. directory "/"
// is the group's root. fileCount is how many entries to return this page.
func BuildGroupFileList(groupUin uint32, directory string, startIndex, fileCount uint32) ([]byte, string) {
	if directory == "" {
		directory = "/"
	}
	if fileCount == 0 {
		fileCount = 20
	}
	body := Marshal(BytesField(2, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(2, groupFSAppID),
		StringField(3, directory),
		VarintField(5, uint64(fileCount)),
		VarintField(9, 1),
		VarintField(13, uint64(startIndex)),
		VarintField(17, 2),
		VarintField(18, 0),
	)))
	return buildOidbReserved(0x6d8, 1, body, 1), OidbCommand(0x6d8, 1)
}

// BuildGroupFileCount asks 0x6d8_2 how many files the group holds.
func BuildGroupFileCount(groupUin uint32) ([]byte, string) {
	body := Marshal(BytesField(3, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(2, groupFSAppID),
		VarintField(3, 6),
	)))
	return buildOidbReserved(0x6d8, 2, body, 1), OidbCommand(0x6d8, 2)
}

// BuildGroupFileSpace asks 0x6d8_3 for used/total space.
func BuildGroupFileSpace(groupUin uint32) ([]byte, string) {
	body := Marshal(BytesField(4, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(2, groupFSAppID),
	)))
	return buildOidbReserved(0x6d8, 3, body, 1), OidbCommand(0x6d8, 3)
}

// BuildGroupFileDownload asks 0x6d6_2 for a download URL.
func BuildGroupFileDownload(groupUin uint32, fileID string, busIDs ...uint32) ([]byte, string) {
	busID := uint32(groupFSBusID)
	if len(busIDs) > 0 && busIDs[0] != 0 {
		busID = busIDs[0]
	}
	body := Marshal(BytesField(3, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(2, groupFSAppID),
		VarintField(3, uint64(busID)),
		StringField(4, fileID),
	)))
	return buildOidbReserved(0x6d6, 2, body, 1), OidbCommand(0x6d6, 2)
}

// BuildGroupFileDelete removes a file: 0x6d6_3.
func BuildGroupFileDelete(groupUin uint32, fileID string, busID uint32) ([]byte, string) {
	if busID == 0 {
		busID = groupFSBusID
	}
	body := Marshal(BytesField(4, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(3, uint64(busID)),
		StringField(5, fileID),
	)))
	return buildOidbReserved(0x6d6, 3, body, 1), OidbCommand(0x6d6, 3)
}

// BuildGroupFolderCreate makes a folder: 0x6d7_0. parent "/" is the root.
func BuildGroupFolderCreate(groupUin uint32, parent, name string) ([]byte, string) {
	if parent == "" {
		parent = "/"
	}
	body := Marshal(BytesField(1, Marshal(
		VarintField(1, uint64(groupUin)),
		StringField(3, parent),
		StringField(4, name),
	)))
	return buildOidbReserved(0x6d7, 0, body, 1), OidbCommand(0x6d7, 0)
}

// BuildGroupFolderDelete removes a folder: 0x6d7_1.
func BuildGroupFolderDelete(groupUin uint32, folderID string) ([]byte, string) {
	body := Marshal(BytesField(2, Marshal(
		VarintField(1, uint64(groupUin)),
		StringField(3, folderID),
	)))
	return buildOidbReserved(0x6d7, 1, body, 1), OidbCommand(0x6d7, 1)
}

// BuildGroupFileRename renames a file: 0x6d6_4. parent "/" is the root.
func BuildGroupFileRename(groupUin uint32, fileID, parent, name string, busID uint32) ([]byte, string) {
	if busID == 0 {
		busID = groupFSBusID
	}
	if parent == "" {
		parent = "/"
	}
	body := Marshal(BytesField(5, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(3, uint64(busID)),
		StringField(4, fileID),
		StringField(5, parent),
		StringField(6, name),
	)))
	return buildOidbReserved(0x6d6, 4, body, 1), OidbCommand(0x6d6, 4)
}

// BuildGroupFileMove moves a file: 0x6d6_5. parent "/" is the current directory.
func BuildGroupFileMove(groupUin uint32, fileID, parent, target string, busID uint32) ([]byte, string) {
	if busID == 0 {
		busID = groupFSBusID
	}
	if parent == "" {
		parent = "/"
	}
	body := Marshal(BytesField(6, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(3, uint64(busID)),
		StringField(4, fileID),
		StringField(5, parent),
		StringField(6, target),
	)))
	return buildOidbReserved(0x6d6, 5, body, 1), OidbCommand(0x6d6, 5)
}

// BuildGroupFolderRename renames a folder: 0x6d7_2.
func BuildGroupFolderRename(groupUin uint32, folderID, name string) ([]byte, string) {
	body := Marshal(BytesField(3, Marshal(
		VarintField(1, uint64(groupUin)),
		StringField(3, folderID),
		StringField(4, name),
	)))
	return buildOidbReserved(0x6d7, 2, body, 1), OidbCommand(0x6d7, 2)
}

// BuildGroupSendFile posts an already-uploaded group file as a group
// message: 0x6d9_4. random is the client field LagrangeGo fills with RandU32.
func BuildGroupSendFile(groupUin uint32, fileID string, random uint32) ([]byte, string) {
	if random == 0 {
		random = 1
	}
	body := Marshal(BytesField(5, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(2, 4),
		BytesField(3, Marshal(
			VarintField(1, groupFSBusID),
			StringField(2, fileID),
			VarintField(3, uint64(random)),
			StringField(4, "{}"),
			VarintField(5, 1),
		)),
	)))
	return buildOidbReserved(0x6d9, 4, body, 1), OidbCommand(0x6d9, 4)
}

// GroupFile is one file in a 0x6d8_1 listing.
type GroupFile struct {
	FileID          string
	FileName        string
	FileSize        uint64
	BusID           uint32
	UploadedTime    uint32
	ExpireTime      uint32
	ModifyTime      uint32
	DownloadTimes   uint32
	UploaderUin     uint32
	UploaderName    string
	ParentDirectory string
	MD5             string
}

// GroupFolder is one folder in a 0x6d8_1 listing.
type GroupFolder struct {
	FolderID       string
	ParentID       string
	FolderName     string
	CreateTime     uint32
	ModifyTime     uint32
	CreatorUin     uint32
	CreatorName    string
	TotalFileCount uint32
}

// GroupFileList is one page of 0x6d8_1.
type GroupFileList struct {
	Files   []GroupFile
	Folders []GroupFolder
	IsEnd   bool
}

func fsRetError(f []Field) error {
	if code := pbU32(f, 1); code != 0 {
		msg := pbStr(f, 3)
		if msg == "" {
			msg = pbStr(f, 2)
		}
		if msg == "" {
			msg = fmt.Sprintf("code %d", code)
		}
		return fmt.Errorf("protocol: group file: %s", msg)
	}
	return nil
}

// ParseGroupFileList reads 0x6d8_1.
func ParseGroupFileList(answer []byte) (GroupFileList, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return GroupFileList{}, err
	}
	top, err := EveryField(body)
	if err != nil {
		return GroupFileList{}, fmt.Errorf("protocol: group file list: %w", err)
	}
	if len(pbBytes(top, 2)) == 0 {
		return GroupFileList{}, fmt.Errorf("protocol: missing group file response section")
	}
	list, err := EveryField(pbBytes(top, 2))
	if err != nil {
		return GroupFileList{}, fmt.Errorf("protocol: group file list: %w", err)
	}
	if err := fsRetError(list); err != nil {
		return GroupFileList{}, err
	}
	out := GroupFileList{IsEnd: pbU32(list, 4) != 0}
	for _, item := range FieldsWith(list, 5) {
		if !item.IsData {
			continue
		}
		entry, err := EveryField(item.Data)
		if err != nil {
			return GroupFileList{}, err
		}
		if folder := pbBytes(entry, 2); len(folder) > 0 {
			f, err := EveryField(folder)
			if err != nil {
				return GroupFileList{}, err
			}
			out.Folders = append(out.Folders, GroupFolder{
				FolderID:       pbStr(f, 1),
				ParentID:       pbStr(f, 2),
				FolderName:     pbStr(f, 3),
				CreateTime:     pbU32(f, 4),
				ModifyTime:     pbU32(f, 5),
				CreatorUin:     pbU32(f, 6),
				CreatorName:    pbStr(f, 7),
				TotalFileCount: pbU32(f, 8),
			})
			continue
		}
		file := pbBytes(entry, 3)
		if len(file) == 0 {
			continue
		}
		f, err := EveryField(file)
		if err != nil {
			return GroupFileList{}, err
		}
		out.Files = append(out.Files, GroupFile{
			FileID:          pbStr(f, 1),
			FileName:        pbStr(f, 2),
			FileSize:        pbU64(f, 3),
			BusID:           pbU32(f, 4),
			UploadedTime:    pbU32(f, 6),
			ExpireTime:      pbU32(f, 7),
			ModifyTime:      pbU32(f, 8),
			DownloadTimes:   pbU32(f, 9),
			MD5:             hex.EncodeToString(pbBytes(f, 12)),
			UploaderName:    pbStr(f, 14),
			UploaderUin:     pbU32(f, 15),
			ParentDirectory: pbStr(f, 16),
		})
	}
	return out, nil
}

// ParseGroupFileCount reads 0x6d8_2: file_count, limit_count.
func ParseGroupFileCount(answer []byte) (uint32, uint32, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return 0, 0, err
	}
	top, err := EveryField(body)
	if err != nil {
		return 0, 0, fmt.Errorf("protocol: group file count: %w", err)
	}
	if len(pbBytes(top, 3)) == 0 {
		return 0, 0, fmt.Errorf("protocol: missing group file response section")
	}
	count, err := EveryField(pbBytes(top, 3))
	if err != nil {
		return 0, 0, fmt.Errorf("protocol: group file count: %w", err)
	}
	if err := fsRetError(count); err != nil {
		return 0, 0, err
	}
	return pbU32(count, 4), pbU32(count, 6), nil
}

// ParseGroupFileSpace reads 0x6d8_3: total, used.
func ParseGroupFileSpace(answer []byte) (uint64, uint64, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return 0, 0, err
	}
	top, err := EveryField(body)
	if err != nil {
		return 0, 0, fmt.Errorf("protocol: group file space: %w", err)
	}
	if len(pbBytes(top, 4)) == 0 {
		return 0, 0, fmt.Errorf("protocol: missing group file response section")
	}
	space, err := EveryField(pbBytes(top, 4))
	if err != nil {
		return 0, 0, fmt.Errorf("protocol: group file space: %w", err)
	}
	if err := fsRetError(space); err != nil {
		return 0, 0, err
	}
	return pbU64(space, 4), pbU64(space, 5), nil
}

// ParseGroupFileDownload reads 0x6d6_2 into an https ftn_handler URL.
func ParseGroupFileDownload(answer []byte) (string, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return "", err
	}
	top, err := EveryField(body)
	if err != nil {
		return "", fmt.Errorf("protocol: group file download: %w", err)
	}
	dl, err := EveryField(pbBytes(top, 3))
	if err != nil {
		return "", fmt.Errorf("protocol: group file download: %w", err)
	}
	if err := fsRetError(dl); err != nil {
		return "", err
	}
	// Field 4 is an IP endpoint; field 5 is the TLS download hostname.
	host := pbStr(dl, 5)
	if host == "" {
		host = pbStr(dl, 4)
	}
	if host == "" {
		return "", fmt.Errorf("protocol: group file download: empty host")
	}
	return fmt.Sprintf("https://%s:443/ftn_handler/%s/?fname=", host, hex.EncodeToString(pbBytes(dl, 6))), nil
}

// ParseGroupFileDelete reads 0x6d6_3 / 0x6d7_1.
func ParseGroupFileDelete(answer []byte, tag int) error {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return err
	}
	top, err := EveryField(body)
	if err != nil {
		return fmt.Errorf("protocol: group file delete: %w", err)
	}
	inner, err := EveryField(pbBytes(top, tag))
	if err != nil {
		return fmt.Errorf("protocol: group file delete: %w", err)
	}
	return fsRetError(inner)
}

// ParseGroupFolderCreate reads 0x6d7_0.
func ParseGroupFolderCreate(answer []byte) (GroupFolder, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return GroupFolder{}, err
	}
	top, err := EveryField(body)
	if err != nil {
		return GroupFolder{}, fmt.Errorf("protocol: group folder create: %w", err)
	}
	created, err := EveryField(pbBytes(top, 1))
	if err != nil {
		return GroupFolder{}, fmt.Errorf("protocol: group folder create: %w", err)
	}
	if err := fsRetError(created); err != nil {
		return GroupFolder{}, err
	}
	info, err := EveryField(pbBytes(created, 4))
	if err != nil {
		return GroupFolder{}, fmt.Errorf("protocol: group folder create: %w", err)
	}
	return GroupFolder{
		FolderID:   pbStr(info, 1),
		ParentID:   pbStr(info, 2),
		FolderName: pbStr(info, 3),
		CreateTime: pbU32(info, 4),
		ModifyTime: pbU32(info, 5),
		CreatorUin: pbU32(info, 6),
	}, nil
}
