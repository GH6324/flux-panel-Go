package service

import (
	"flux-panel/go-backend/dto"
	"flux-panel/go-backend/model"
	"flux-panel/go-backend/pkg"
	"sort"
	"time"
)

// GetAdminDashboardStats returns aggregated stats for admin dashboard.
func GetAdminDashboardStats() dto.R {
	// Nodes — use live WS status for accurate online count
	var allNodes []model.Node
	DB.Find(&allNodes)
	totalNodes := int64(len(allNodes))
	var onlineNodes int64
	for _, n := range allNodes {
		if pkg.WS != nil && pkg.WS.IsNodeOnline(n.ID) {
			onlineNodes++
		}
	}

	// Users (non-admin)
	var totalUsers int64
	DB.Model(&model.User{}).Where("role_id != 0").Count(&totalUsers)

	// Forwards
	var totalForwards, activeForwards int64
	DB.Model(&model.Forward{}).Count(&totalForwards)
	DB.Model(&model.Forward{}).Where("status = 1").Count(&activeForwards)

	// Traffic history + today's traffic (computed from same snapshot data)
	trafficData := getTrafficData(0)
	xrayData := getXrayTrafficData(0)

	// Monthly traffic
	monthlyGost, monthlyXray := getMonthlyTraffic()

	// Node traffic ranking
	nodeTrafficRank := getNodeTrafficRanking()

	// Top 5 users by monthly traffic
	topUsers := getUserMonthlyTrafficRanking()

	return dto.Ok(map[string]interface{}{
		"nodes":               map[string]int64{"total": totalNodes, "online": onlineNodes},
		"users":               map[string]int64{"total": totalUsers},
		"forwards":            map[string]int64{"total": totalForwards, "active": activeForwards},
		"todayTraffic":        trafficData.todayFlow,
		"trafficHistory":      trafficData.history,
		"todayXrayTraffic":    xrayData.todayFlow,
		"xrayTrafficHistory":  xrayData.history,
		"monthlyGostTraffic":  monthlyGost,
		"monthlyXrayTraffic":  monthlyXray,
		"topUsers":            topUsers,
		"nodeTrafficRank":     nodeTrafficRank,
	})
}

// GetUserDashboardStats returns stats for a regular user.
func GetUserDashboardStats(userId int64) dto.R {
	// User package info
	var user model.User
	if err := DB.First(&user, userId).Error; err != nil {
		return dto.Err("用户不存在")
	}

	packageInfo := map[string]interface{}{
		"flow":          user.Flow,
		"inFlow":        user.InFlow,
		"outFlow":       user.OutFlow,
		"vFlow":         user.XrayFlow,
		"vInFlow":       user.XrayInFlow,
		"vOutFlow":      user.XrayOutFlow,
		"num":           user.Num,
		"expTime":       user.ExpTime,
		"flowResetType": user.FlowResetType,
		"flowResetDay":  user.FlowResetDay,
	}

	// Forward count
	var forwardCount int64
	DB.Model(&model.Forward{}).Where("user_id = ?", userId).Count(&forwardCount)

	// Traffic history for this user (per-user snapshots for accurate data)
	gostData, xrayData := getUserFlowData(userId)

	return dto.Ok(map[string]interface{}{
		"package":            packageInfo,
		"forwards":           forwardCount,
		"trafficHistory":     gostData.history,
		"xrayTrafficHistory": xrayData.history,
	})
}

// trafficData holds both 24h traffic history and today's total.
type trafficData struct {
	history  []map[string]interface{}
	todayFlow int64
}

// getTrafficData returns 24h traffic history and today's total from statistics_forward_flow.
// Uses cumulative snapshots with delta computation (same approach as monitor page).
// If userId=0, aggregates all forwards; otherwise filters by user's forwards.
func getTrafficData(userId int64) trafficData {
	cutoff := time.Now().Unix() - 25*3600 // fetch one extra hour for delta computation

	var records []model.StatisticsForwardFlow
	query := DB.Where("record_time >= ?", cutoff).Order("record_time ASC")
	if userId > 0 {
		var forwardIds []int64
		DB.Model(&model.Forward{}).Where("user_id = ?", userId).Pluck("id", &forwardIds)
		if len(forwardIds) == 0 {
			return trafficData{history: buildEmptyTrafficHistory()}
		}
		query = query.Where("forward_id IN ?", forwardIds)
	}
	query.Find(&records)

	if len(records) == 0 {
		return trafficData{history: buildEmptyTrafficHistory()}
	}

	// Group by (forwardId, bucket) → last snapshot
	bucketSize := int64(3600)
	type fwBucketKey struct {
		ForwardId int64
		Bucket    int64
	}
	fwBucketSnapshot := make(map[fwBucketKey]int64) // total flow
	for _, r := range records {
		key := fwBucketKey{r.ForwardId, (r.RecordTime / bucketSize) * bucketSize}
		fwBucketSnapshot[key] = r.InFlow + r.OutFlow
	}

	// Collect unique forward IDs and sorted buckets
	fwIds := make(map[int64]bool)
	allBuckets := make(map[int64]bool)
	for k := range fwBucketSnapshot {
		fwIds[k.ForwardId] = true
		allBuckets[k.Bucket] = true
	}

	sortedBuckets := make([]int64, 0, len(allBuckets))
	for b := range allBuckets {
		sortedBuckets = append(sortedBuckets, b)
	}
	sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

	// Compute deltas per bucket
	actualCutoff := time.Now().Unix() - 24*3600
	bucketFlow := make(map[int64]int64) // bucket → total delta flow
	for fwId := range fwIds {
		var prev int64
		firstSeen := false
		for _, bt := range sortedBuckets {
			snap, ok := fwBucketSnapshot[fwBucketKey{fwId, bt}]
			if !ok {
				continue
			}
			if !firstSeen {
				prev = snap
				firstSeen = true
				continue
			}
			if bt < actualCutoff {
				prev = snap
				continue
			}
			delta := snap - prev
			if delta < 0 {
				delta = 0
			}
			bucketFlow[bt] += delta
			prev = snap
		}
	}

	// Build 24-hour result — return Unix timestamps, let frontend format in browser timezone
	now := time.Now()
	nowTs := now.Unix()
	result := make([]map[string]interface{}, 0, 24)
	for i := 23; i >= 0; i-- {
		bt := ((nowTs - int64(i)*3600) / bucketSize) * bucketSize
		result = append(result, map[string]interface{}{
			"time": bt,
			"flow": bucketFlow[bt],
		})
	}

	// Sum today's traffic — use UTC-based "last 24h" to avoid server-timezone dependency
	var todayTotal int64
	cutoff24h := nowTs - 24*3600
	for bt, flow := range bucketFlow {
		if bt >= cutoff24h {
			todayTotal += flow
		}
	}

	return trafficData{history: result, todayFlow: todayTotal}
}

// getXrayTrafficData returns 24h Xray traffic history and today's total from statistics_xray_flow.
// Uses cumulative snapshots with delta computation (same approach as GOST).
// If userId=0, aggregates all inbounds; otherwise filters by user's inbound IDs.
func getXrayTrafficData(userId int64) trafficData {
	cutoff := time.Now().Unix() - 25*3600

	var records []model.StatisticsXrayFlow
	query := DB.Where("record_time >= ?", cutoff).Order("record_time ASC")
	if userId > 0 {
		var inboundIds []int64
		DB.Model(&model.XrayClient{}).Where("user_id = ?", userId).Distinct("inbound_id").Pluck("inbound_id", &inboundIds)
		if len(inboundIds) == 0 {
			return trafficData{history: buildEmptyTrafficHistory()}
		}
		query = query.Where("inbound_id IN ?", inboundIds)
	}
	query.Find(&records)

	if len(records) == 0 {
		return trafficData{history: buildEmptyTrafficHistory()}
	}

	bucketSize := int64(3600)
	type ibBucketKey struct {
		InboundId int64
		Bucket    int64
	}
	ibBucketSnapshot := make(map[ibBucketKey]int64) // total flow (up+down)
	for _, r := range records {
		key := ibBucketKey{r.InboundId, (r.RecordTime / bucketSize) * bucketSize}
		ibBucketSnapshot[key] = r.UpFlow + r.DownFlow
	}

	ibIds := make(map[int64]bool)
	allBuckets := make(map[int64]bool)
	for k := range ibBucketSnapshot {
		ibIds[k.InboundId] = true
		allBuckets[k.Bucket] = true
	}

	sortedBuckets := make([]int64, 0, len(allBuckets))
	for b := range allBuckets {
		sortedBuckets = append(sortedBuckets, b)
	}
	sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

	actualCutoff := time.Now().Unix() - 24*3600
	bucketFlow := make(map[int64]int64)
	for ibId := range ibIds {
		var prev int64
		firstSeen := false
		for _, bt := range sortedBuckets {
			snap, ok := ibBucketSnapshot[ibBucketKey{ibId, bt}]
			if !ok {
				continue
			}
			if !firstSeen {
				prev = snap
				firstSeen = true
				continue
			}
			if bt < actualCutoff {
				prev = snap
				continue
			}
			delta := snap - prev
			if delta < 0 {
				delta = 0
			}
			bucketFlow[bt] += delta
			prev = snap
		}
	}

	now := time.Now()
	nowTs := now.Unix()
	result := make([]map[string]interface{}, 0, 24)
	for i := 23; i >= 0; i-- {
		bt := ((nowTs - int64(i)*3600) / bucketSize) * bucketSize
		result = append(result, map[string]interface{}{
			"time": bt,
			"flow": bucketFlow[bt],
		})
	}

	var todayTotal int64
	cutoff24h := nowTs - 24*3600
	for bt, flow := range bucketFlow {
		if bt >= cutoff24h {
			todayTotal += flow
		}
	}

	return trafficData{history: result, todayFlow: todayTotal}
}

// getUserFlowData returns 24h GOST and Xray traffic history for a specific user
// using per-user flow snapshots (accurate, not inflated by shared inbounds).
func getUserFlowData(userId int64) (gostData trafficData, xrayData trafficData) {
	cutoff := time.Now().Unix() - 25*3600

	var records []model.StatisticsUserFlow
	DB.Where("user_id = ? AND record_time >= ?", userId, cutoff).
		Order("record_time ASC").
		Find(&records)

	if len(records) == 0 {
		return trafficData{history: buildEmptyTrafficHistory()}, trafficData{history: buildEmptyTrafficHistory()}
	}

	bucketSize := int64(3600)
	type bucketSnap struct {
		GostFlow int64
		XrayFlow int64
	}
	bucketSnapshot := make(map[int64]bucketSnap)
	for _, r := range records {
		bt := (r.RecordTime / bucketSize) * bucketSize
		bucketSnapshot[bt] = bucketSnap{r.GostFlow, r.XrayFlow}
	}

	sortedBuckets := make([]int64, 0, len(bucketSnapshot))
	for b := range bucketSnapshot {
		sortedBuckets = append(sortedBuckets, b)
	}
	sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

	actualCutoff := time.Now().Unix() - 24*3600
	gostBucketFlow := make(map[int64]int64)
	xrayBucketFlow := make(map[int64]int64)

	var prevGost, prevXray int64
	firstSeen := false
	for _, bt := range sortedBuckets {
		snap := bucketSnapshot[bt]
		if !firstSeen {
			prevGost = snap.GostFlow
			prevXray = snap.XrayFlow
			firstSeen = true
			continue
		}
		if bt < actualCutoff {
			prevGost = snap.GostFlow
			prevXray = snap.XrayFlow
			continue
		}
		gostDelta := snap.GostFlow - prevGost
		xrayDelta := snap.XrayFlow - prevXray
		if gostDelta < 0 {
			gostDelta = 0
		}
		if xrayDelta < 0 {
			xrayDelta = 0
		}
		gostBucketFlow[bt] = gostDelta
		xrayBucketFlow[bt] = xrayDelta
		prevGost = snap.GostFlow
		prevXray = snap.XrayFlow
	}

	now := time.Now()
	nowTs := now.Unix()
	gostHistory := make([]map[string]interface{}, 0, 24)
	xrayHistory := make([]map[string]interface{}, 0, 24)
	for i := 23; i >= 0; i-- {
		bt := ((nowTs - int64(i)*3600) / bucketSize) * bucketSize
		gostHistory = append(gostHistory, map[string]interface{}{
			"time": bt,
			"flow": gostBucketFlow[bt],
		})
		xrayHistory = append(xrayHistory, map[string]interface{}{
			"time": bt,
			"flow": xrayBucketFlow[bt],
		})
	}

	var gostTotal, xrayTotal int64
	cutoff24h := nowTs - 24*3600
	for bt, flow := range gostBucketFlow {
		if bt >= cutoff24h {
			gostTotal += flow
		}
	}
	for bt, flow := range xrayBucketFlow {
		if bt >= cutoff24h {
			xrayTotal += flow
		}
	}

	return trafficData{history: gostHistory, todayFlow: gostTotal},
		trafficData{history: xrayHistory, todayFlow: xrayTotal}
}

// getMonthlyTraffic returns this calendar month's GOST + Xray traffic.
func getMonthlyTraffic() (gostMonthly int64, xrayMonthly int64) {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	cutoff := monthStart - 3600 // one extra hour for delta base

	// GOST monthly traffic
	var gostRecords []model.StatisticsForwardFlow
	DB.Where("record_time >= ?", cutoff).Order("record_time ASC").Find(&gostRecords)

	if len(gostRecords) > 0 {
		bucketSize := int64(3600)
		type fwBucketKey struct {
			ForwardId int64
			Bucket    int64
		}
		fwBucketSnapshot := make(map[fwBucketKey]int64)
		for _, r := range gostRecords {
			key := fwBucketKey{r.ForwardId, (r.RecordTime / bucketSize) * bucketSize}
			fwBucketSnapshot[key] = r.InFlow + r.OutFlow
		}

		fwIds := make(map[int64]bool)
		allBuckets := make(map[int64]bool)
		for k := range fwBucketSnapshot {
			fwIds[k.ForwardId] = true
			allBuckets[k.Bucket] = true
		}
		sortedBuckets := make([]int64, 0, len(allBuckets))
		for b := range allBuckets {
			sortedBuckets = append(sortedBuckets, b)
		}
		sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

		for fwId := range fwIds {
			var prev int64
			firstSeen := false
			for _, bt := range sortedBuckets {
				snap, ok := fwBucketSnapshot[fwBucketKey{fwId, bt}]
				if !ok {
					continue
				}
				if !firstSeen {
					prev = snap
					firstSeen = true
					continue
				}
				if bt < monthStart {
					prev = snap
					continue
				}
				delta := snap - prev
				if delta < 0 {
					delta = 0
				}
				gostMonthly += delta
				prev = snap
			}
		}
	}

	// Xray monthly traffic
	var xrayRecords []model.StatisticsXrayFlow
	DB.Where("record_time >= ?", cutoff).Order("record_time ASC").Find(&xrayRecords)

	if len(xrayRecords) > 0 {
		bucketSize := int64(3600)
		type ibBucketKey struct {
			InboundId int64
			Bucket    int64
		}
		ibBucketSnapshot := make(map[ibBucketKey]int64)
		for _, r := range xrayRecords {
			key := ibBucketKey{r.InboundId, (r.RecordTime / bucketSize) * bucketSize}
			ibBucketSnapshot[key] = r.UpFlow + r.DownFlow
		}

		ibIds := make(map[int64]bool)
		allBuckets := make(map[int64]bool)
		for k := range ibBucketSnapshot {
			ibIds[k.InboundId] = true
			allBuckets[k.Bucket] = true
		}
		sortedBuckets := make([]int64, 0, len(allBuckets))
		for b := range allBuckets {
			sortedBuckets = append(sortedBuckets, b)
		}
		sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

		for ibId := range ibIds {
			var prev int64
			firstSeen := false
			for _, bt := range sortedBuckets {
				snap, ok := ibBucketSnapshot[ibBucketKey{ibId, bt}]
				if !ok {
					continue
				}
				if !firstSeen {
					prev = snap
					firstSeen = true
					continue
				}
				if bt < monthStart {
					prev = snap
					continue
				}
				delta := snap - prev
				if delta < 0 {
					delta = 0
				}
				xrayMonthly += delta
				prev = snap
			}
		}
	}

	return
}

// getNodeTrafficRanking returns per-node monthly traffic ranking.
func getNodeTrafficRanking() []map[string]interface{} {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	cutoff := monthStart - 3600

	// Build node name map
	var allNodes []model.Node
	DB.Find(&allNodes)
	nodeNameMap := make(map[int64]string)
	for _, n := range allNodes {
		nodeNameMap[n.ID] = n.Name
	}

	// GOST: Forward → Tunnel(tunnel_id) → Node(in_node_id)
	type fwTunnel struct {
		ForwardId int64
		NodeId    int64
	}
	var fwTunnelRows []fwTunnel
	DB.Model(&model.Forward{}).
		Select("forward.id as forward_id, tunnel.in_node_id as node_id").
		Joins("JOIN tunnel ON tunnel.id = forward.tunnel_id").
		Find(&fwTunnelRows)

	fwToNode := make(map[int64]int64)
	for _, ft := range fwTunnelRows {
		fwToNode[ft.ForwardId] = ft.NodeId
	}

	// Compute per-forward GOST monthly delta
	var gostRecords []model.StatisticsForwardFlow
	DB.Where("record_time >= ?", cutoff).Order("record_time ASC").Find(&gostRecords)

	nodeGostFlow := make(map[int64]int64)
	if len(gostRecords) > 0 {
		bucketSize := int64(3600)
		type fwBucketKey struct {
			ForwardId int64
			Bucket    int64
		}
		fwBucketSnapshot := make(map[fwBucketKey]int64)
		for _, r := range gostRecords {
			key := fwBucketKey{r.ForwardId, (r.RecordTime / bucketSize) * bucketSize}
			fwBucketSnapshot[key] = r.InFlow + r.OutFlow
		}

		fwIds := make(map[int64]bool)
		allBuckets := make(map[int64]bool)
		for k := range fwBucketSnapshot {
			fwIds[k.ForwardId] = true
			allBuckets[k.Bucket] = true
		}
		sortedBuckets := make([]int64, 0, len(allBuckets))
		for b := range allBuckets {
			sortedBuckets = append(sortedBuckets, b)
		}
		sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

		fwFlow := make(map[int64]int64) // per-forward monthly flow
		for fwId := range fwIds {
			var prev int64
			firstSeen := false
			for _, bt := range sortedBuckets {
				snap, ok := fwBucketSnapshot[fwBucketKey{fwId, bt}]
				if !ok {
					continue
				}
				if !firstSeen {
					prev = snap
					firstSeen = true
					continue
				}
				if bt < monthStart {
					prev = snap
					continue
				}
				delta := snap - prev
				if delta < 0 {
					delta = 0
				}
				fwFlow[fwId] += delta
				prev = snap
			}
		}
		for fwId, flow := range fwFlow {
			if nodeId, ok := fwToNode[fwId]; ok {
				nodeGostFlow[nodeId] += flow
			}
		}
	}

	// Xray: XrayInbound has nodeId directly
	type ibNode struct {
		InboundId int64
		NodeId    int64
	}
	var ibNodeRows []ibNode
	DB.Model(&model.XrayInbound{}).Select("id as inbound_id, node_id").Find(&ibNodeRows)
	ibToNode := make(map[int64]int64)
	for _, in := range ibNodeRows {
		ibToNode[in.InboundId] = in.NodeId
	}

	var xrayRecords []model.StatisticsXrayFlow
	DB.Where("record_time >= ?", cutoff).Order("record_time ASC").Find(&xrayRecords)

	nodeXrayFlow := make(map[int64]int64)
	if len(xrayRecords) > 0 {
		bucketSize := int64(3600)
		type ibBucketKey struct {
			InboundId int64
			Bucket    int64
		}
		ibBucketSnapshot := make(map[ibBucketKey]int64)
		for _, r := range xrayRecords {
			key := ibBucketKey{r.InboundId, (r.RecordTime / bucketSize) * bucketSize}
			ibBucketSnapshot[key] = r.UpFlow + r.DownFlow
		}

		ibIds := make(map[int64]bool)
		allBuckets := make(map[int64]bool)
		for k := range ibBucketSnapshot {
			ibIds[k.InboundId] = true
			allBuckets[k.Bucket] = true
		}
		sortedBuckets := make([]int64, 0, len(allBuckets))
		for b := range allBuckets {
			sortedBuckets = append(sortedBuckets, b)
		}
		sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

		ibFlow := make(map[int64]int64)
		for ibId := range ibIds {
			var prev int64
			firstSeen := false
			for _, bt := range sortedBuckets {
				snap, ok := ibBucketSnapshot[ibBucketKey{ibId, bt}]
				if !ok {
					continue
				}
				if !firstSeen {
					prev = snap
					firstSeen = true
					continue
				}
				if bt < monthStart {
					prev = snap
					continue
				}
				delta := snap - prev
				if delta < 0 {
					delta = 0
				}
				ibFlow[ibId] += delta
				prev = snap
			}
		}
		for ibId, flow := range ibFlow {
			if nodeId, ok := ibToNode[ibId]; ok {
				nodeXrayFlow[nodeId] += flow
			}
		}
	}

	// Merge and build ranking
	allNodeIds := make(map[int64]bool)
	for id := range nodeGostFlow {
		allNodeIds[id] = true
	}
	for id := range nodeXrayFlow {
		allNodeIds[id] = true
	}

	type nodeRank struct {
		NodeId    int64
		NodeName  string
		GostFlow  int64
		XrayFlow  int64
		TotalFlow int64
	}
	ranking := make([]nodeRank, 0, len(allNodeIds))
	for id := range allNodeIds {
		gf := nodeGostFlow[id]
		xf := nodeXrayFlow[id]
		ranking = append(ranking, nodeRank{
			NodeId:    id,
			NodeName:  nodeNameMap[id],
			GostFlow:  gf,
			XrayFlow:  xf,
			TotalFlow: gf + xf,
		})
	}
	sort.Slice(ranking, func(i, j int) bool { return ranking[i].TotalFlow > ranking[j].TotalFlow })

	result := make([]map[string]interface{}, 0, len(ranking))
	for _, r := range ranking {
		result = append(result, map[string]interface{}{
			"nodeId":    r.NodeId,
			"nodeName":  r.NodeName,
			"gostFlow":  r.GostFlow,
			"xrayFlow":  r.XrayFlow,
			"totalFlow": r.TotalFlow,
		})
	}
	return result
}

// getUserMonthlyTrafficRanking returns top 5 users by this month's traffic using delta computation.
func getUserMonthlyTrafficRanking() []map[string]interface{} {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	cutoff := monthStart - 3600

	// Build user name map
	type userInfo struct {
		ID   int64
		User string
	}
	var users []userInfo
	DB.Model(&model.User{}).Select("id, `user`").Where("role_id != 0").Find(&users)
	userNameMap := make(map[int64]string)
	for _, u := range users {
		userNameMap[u.ID] = u.User
	}

	var records []model.StatisticsUserFlow
	DB.Where("record_time >= ?", cutoff).Order("record_time ASC").Find(&records)

	if len(records) == 0 {
		return []map[string]interface{}{}
	}

	bucketSize := int64(3600)
	type userBucketKey struct {
		UserId int64
		Bucket int64
	}
	type flowSnap struct {
		GostFlow int64
		XrayFlow int64
	}
	userBucketSnapshot := make(map[userBucketKey]flowSnap)
	for _, r := range records {
		key := userBucketKey{r.UserId, (r.RecordTime / bucketSize) * bucketSize}
		userBucketSnapshot[key] = flowSnap{r.GostFlow, r.XrayFlow}
	}

	userIds := make(map[int64]bool)
	allBuckets := make(map[int64]bool)
	for k := range userBucketSnapshot {
		userIds[k.UserId] = true
		allBuckets[k.Bucket] = true
	}
	sortedBuckets := make([]int64, 0, len(allBuckets))
	for b := range allBuckets {
		sortedBuckets = append(sortedBuckets, b)
	}
	sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

	type userFlow struct {
		GostFlow int64
		XrayFlow int64
	}
	userMonthly := make(map[int64]*userFlow)
	for uid := range userIds {
		var prevGost, prevXray int64
		firstSeen := false
		for _, bt := range sortedBuckets {
			snap, ok := userBucketSnapshot[userBucketKey{uid, bt}]
			if !ok {
				continue
			}
			if !firstSeen {
				prevGost = snap.GostFlow
				prevXray = snap.XrayFlow
				firstSeen = true
				continue
			}
			if bt < monthStart {
				prevGost = snap.GostFlow
				prevXray = snap.XrayFlow
				continue
			}
			gd := snap.GostFlow - prevGost
			xd := snap.XrayFlow - prevXray
			if gd < 0 {
				gd = 0
			}
			if xd < 0 {
				xd = 0
			}
			if userMonthly[uid] == nil {
				userMonthly[uid] = &userFlow{}
			}
			userMonthly[uid].GostFlow += gd
			userMonthly[uid].XrayFlow += xd
			prevGost = snap.GostFlow
			prevXray = snap.XrayFlow
		}
	}

	type rankEntry struct {
		Name      string
		GostFlow  int64
		XrayFlow  int64
		TotalFlow int64
	}
	ranking := make([]rankEntry, 0, len(userMonthly))
	for uid, uf := range userMonthly {
		total := uf.GostFlow + uf.XrayFlow
		if total <= 0 {
			continue
		}
		ranking = append(ranking, rankEntry{
			Name:      userNameMap[uid],
			GostFlow:  uf.GostFlow,
			XrayFlow:  uf.XrayFlow,
			TotalFlow: total,
		})
	}
	sort.Slice(ranking, func(i, j int) bool { return ranking[i].TotalFlow > ranking[j].TotalFlow })

	if len(ranking) > 5 {
		ranking = ranking[:5]
	}

	result := make([]map[string]interface{}, 0, len(ranking))
	for _, r := range ranking {
		result = append(result, map[string]interface{}{
			"name":     r.Name,
			"flow":     r.TotalFlow,
			"gostFlow": r.GostFlow,
			"vFlow":    r.XrayFlow,
		})
	}
	return result
}

func buildEmptyTrafficHistory() []map[string]interface{} {
	result := make([]map[string]interface{}, 0, 24)
	nowTs := time.Now().Unix()
	bucketSize := int64(3600)
	for i := 23; i >= 0; i-- {
		bt := ((nowTs - int64(i)*3600) / bucketSize) * bucketSize
		result = append(result, map[string]interface{}{
			"time": bt,
			"flow": int64(0),
		})
	}
	return result
}
