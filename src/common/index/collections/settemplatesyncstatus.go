/*
 * Tencent is pleased to support the open source community by making 蓝鲸 available.
 * Copyright (C) 2017-2018 THL A29 Limited, a Tencent company. All rights reserved.
 * Licensed under the MIT License (the "License"); you may not use this file except
 * in compliance with the License. You may obtain a copy of the License at
 * http://opensource.org/licenses/MIT
 * Unless required by applicable law or agreed to in writing, software distributed under
 * the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
 * either express or implied. See the License for the specific language governing permissions and
 * limitations under the License.
 */

package collections

import (
	"configcenter/src/common"
	"configcenter/src/storage/dal/types"
)

func init() {

	// 先注册未规范化的索引，如果索引出现冲突旧，删除未规范化的索引
	registerIndexes(common.BKTableNameSetTemplateSyncStatus, deprecatedSetTemplateSyncStatusIndexes)
	registerIndexes(common.BKTableNameSetTemplateSyncStatus, commSetTemplateSyncStatusIndexes)

}

//  新加和修改后的索引,索引名字一定要用对用的前缀，CCLogicUniqueIdxNamePrefix|common.CCLogicIndexNamePrefix

var commSetTemplateSyncStatusIndexes = []types.Index{}

// deprecated 未规范化前的索引，只允许删除不允许新加和修改，
var deprecatedSetTemplateSyncStatusIndexes = []types.Index{
	{
		Name: "idx_taskID",
		Keys: map[string]int32{
			"task_id": 1,
		},
		Background: true,
	},
	{
		Name: "idx_setID",
		Keys: map[string]int32{
			"bk_set_id": 1,
		},
		Unique:     true,
		Background: true,
	},
	{
		Name: "idx_createLastTime",
		Keys: map[string]int32{
			"last_time":   1,
			"create_time": 1,
		},
		Background: true,
	},
	{
		Name: "idx_status",
		Keys: map[string]int32{
			"status": 1,
		},
		Background: true,
	},
}