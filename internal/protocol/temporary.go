package protocol

func mediaPrivateScene(uid string, groups []uint32) []byte {
	fields := []Field{VarintField(1, 2), StringField(2, uid)}
	if len(groups) > 0 && groups[0] != 0 {
		fields = append(fields, BytesField(3, BuildSendRouting(SendTarget{TempGroup: groups[0], Uid: uid})))
	}
	return Marshal(fields...)
}
