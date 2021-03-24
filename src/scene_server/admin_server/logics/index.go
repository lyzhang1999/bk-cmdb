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

package logics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"configcenter/src/common"
	"configcenter/src/common/backbone"
	"configcenter/src/common/blog"
	"configcenter/src/common/index"
	"configcenter/src/common/metadata"
	"configcenter/src/common/util"
	"configcenter/src/storage/dal"
	"configcenter/src/storage/dal/types"
)

/*
 如何展示错误给用户
*/

func DBSync(e *backbone.Engine, db dal.RDB) {
	go RunSyncDBTableIndex(context.Background(), e, db)
}

type dbTable struct {
	db  dal.RDB
	rid string
}

func RunSyncDBTableIndex(ctx context.Context, e *backbone.Engine, db dal.RDB) {
	for {
		rid := util.GenerateRID()
		dt := &dbTable{db: db, rid: rid}
		blog.Infof("start sync table and index rid: %s", rid)

		if !e.ServiceManageInterface.IsMaster() {
			blog.Infof("skip sync table and index. reason: not master. rid: %s", rid)
			time.Sleep(20 * time.Second)
			continue

		}
		blog.Infof("start object sharding table rid: %s", rid)
		// 先处理模型实例和关联关系表
		if err := dt.syncModelShardingTable(ctx); err != nil {
			blog.Errorf("model table sync error. err: %s, rid: %s", err.Error(), dt.rid)

		}
		blog.Infof("start table common index rid: %s", rid)
		if err := dt.syncIndexes(ctx); err != nil {
			blog.Errorf("model table sync error. err: %s, rid: %s", err.Error(), dt.rid)
		}

		blog.Infof("end sync table and index rid: %s", rid)
		time.Sleep(time.Hour * 12)

	}
}

// 同步表中定义的索引
func (dt *dbTable) syncIndexes(ctx context.Context) error {
	if err := dt.syncDBTableIndexes(ctx); err != nil {
		blog.Warnf("sync table index to db error. err: %s, rid: %s", err.Error(), dt.rid)
		// 不影响后需任务
	}

	return nil

}

func (dt *dbTable) syncDBTableIndexes(ctx context.Context) error {
	deprecatedIndexNames := index.DeprecatedIndexName()
	tableIndexes := index.TableIndexes()

	for tableName, indexes := range tableIndexes {
		deprecatedTableIndexNames := deprecatedIndexNames[tableName]
		if err := dt.syncIndexesToDB(ctx, tableName, indexes, deprecatedTableIndexNames); err != nil {
			blog.Warnf("sync table (%s) index error. err: %s, rid: %s", tableName, err.Error(), dt.rid)
			continue
		}

	}

	return nil
}

// 返回的数据只有common.BKPropertyIDField, common.BKPropertyTypeField, common.BKFieldID 三个字段
func (dt *dbTable) findObjAttrs(ctx context.Context, objID string) ([]metadata.Attribute, error) {
	// 获取字段类型,只需要共有字段
	attrFilter := map[string]interface{}{
		common.BKObjIDField: objID,
		common.BKAppIDField: 0,
	}
	attrs := make([]metadata.Attribute, 0)
	fields := []string{common.BKPropertyIDField, common.BKPropertyTypeField, common.BKFieldID}
	if err := dt.db.Table(common.BKTableNameObjAttDes).Find(attrFilter).Fields(fields...).All(ctx, &attrs); err != nil {
		newErr := fmt.Errorf("get obj(%s) property error. err: %s", objID, err.Error())
		blog.Errorf("%s, rid: %s", newErr.Error(), dt.rid)
		return nil, newErr
	}

	return attrs, nil
}

func (dt *dbTable) syncIndexesToDB(ctx context.Context, tableName string,
	logicIndexes []types.Index, deprecatedTableIndexNames []string) (err error) {
	dbIndexList, err := dt.db.Table(tableName).Indexes(ctx)
	if err != nil {
		blog.Errorf("find db table(%s) index list error. err: %s, rid: %s", tableName, err.Error(), dt.rid)
		return err
	}

	dbIdxNameMap := make(map[string]types.Index, len(dbIndexList))
	// 等待删除处理索引名字,所有规范后索引名字
	waitDelIdxNameMap := make(map[string]struct{}, 0)
	for _, index := range dbIndexList {

		dbIdxNameMap[index.Name] = index
		if strings.HasPrefix(index.Name, common.CCLogicIndexNamePrefix) {
			waitDelIdxNameMap[index.Name] = struct{}{}
		}

	}
	// TODO: 所有不规范索引的索引名字, 也加入进来
	for _, indexName := range deprecatedTableIndexNames {
		waitDelIdxNameMap[indexName] = struct{}{}
	}

	for _, logicIndex := range logicIndexes {
		// 是否存在相同key的索引
		indexNameExist := false
		// 是否存在同名
		dbIndex, indexNameExist := dbIdxNameMap[logicIndex.Name]
		if indexNameExist {
			delete(waitDelIdxNameMap, logicIndex.Name)
			if err := dt.tryUpdateTableIndex(ctx, tableName, dbIndex, logicIndex); err != nil {
				// 不影响后需执行，
				blog.Error("try update table index err: %s, rid: %s", err.Error(), dt.rid)
				continue
			}
		}

	}

	for indexName := range waitDelIdxNameMap {
		if err := dt.db.Table(tableName).DropIndex(ctx, indexName); err != nil &&
			!ErrDropIndexNameNotFound(err) {
			blog.Errorf("remove redundancy table(%s) index(%s) error. err: %s, rid: %s",
				tableName, indexName, err.Error(), dt.rid)
			return err
		}
	}

	return nil

}

func (dt *dbTable) syncObjTableIndexes(ctx context.Context, tableName string,
	logicIndexes []types.Index, deprecatedTableIndexNames []string, hasUnique bool) (err error) {
	dbIndexList, err := dt.db.Table(tableName).Indexes(ctx)
	if err != nil {
		blog.Errorf("find db table(%s) index list error. err: %s, rid: %s", tableName, err.Error(), dt.rid)
		return err
	}

	dbIdxNameMap := make(map[string]types.Index, len(dbIndexList))
	// 等待删除处理索引名字,所有规范后索引名字
	waitDelIdxNameMap := make(map[string]struct{}, 0)
	for _, index := range dbIndexList {

		dbIdxNameMap[index.Name] = index
		if strings.HasPrefix(index.Name, common.CCLogicIndexNamePrefix) {
			waitDelIdxNameMap[index.Name] = struct{}{}
		}
		if hasUnique && strings.HasPrefix(index.Name, common.CCLogicUniqueIdxNamePrefix) {
			waitDelIdxNameMap[index.Name] = struct{}{}
		}

	}
	for _, indexName := range deprecatedTableIndexNames {
		waitDelIdxNameMap[indexName] = struct{}{}
	}

	for _, logicIndex := range logicIndexes {
		// 是否存在相同key的索引
		indexNameExist := false
		// 是否存在同名
		dbIndex, indexNameExist := dbIdxNameMap[logicIndex.Name]
		if indexNameExist {
			delete(waitDelIdxNameMap, logicIndex.Name)
			if err := dt.tryUpdateTableIndex(ctx, tableName, dbIndex, logicIndex); err != nil {
				// 不影响后需执行，
				blog.Error("try update table index error, index: %s, err: %s, rid: %s", logicIndex, err.Error(), dt.rid)
				continue
			}
		} else {
			dt.createIndexes(ctx, tableName, []types.Index{logicIndex})
		}

	}

	for indexName := range waitDelIdxNameMap {
		if err := dt.db.Table(tableName).DropIndex(ctx, indexName); err != nil &&
			!ErrDropIndexNameNotFound(err) {
			blog.Errorf("remove redundancy table(%s) index(%s) error. err: %s, rid: %s",
				tableName, indexName, err.Error(), dt.rid)
			return err
		}
	}

	return nil

}

func (dt *dbTable) tryUpdateTableIndex(ctx context.Context, tableName string,
	dbIndex, logicIndex types.Index) error {
	if index.IndexEqual(dbIndex, logicIndex) {
		// db collection 中的索引和定义所以的索引一致，无需处理
		return nil
	} else {
		// 说明索引不等， 删除原有的索引，
		if err := dt.db.Table(tableName).DropIndex(ctx, logicIndex.Name); err != nil &&
			!ErrDropIndexNameNotFound(err) {
			blog.Errorf("remove table(%s) index(%s) error. err: %s, rid: %s",
				tableName, logicIndex.Name, err.Error(), dt.rid)
			return err
		}
		if err := dt.db.Table(tableName).CreateIndex(ctx, logicIndex); err != nil {
			blog.Errorf("create table(%s) index(%s) error. err: %s, rid: %s",
				tableName, logicIndex.Name, err.Error(), dt.rid)
			return err
		}
	}
	return nil
}

func (dt *dbTable) syncModelShardingTable(ctx context.Context) error {
	allDBTables, err := dt.db.ListTables(ctx)
	if err != nil {
		blog.Errorf("show tables error. err: %s, rid: %s", err.Error(), dt.rid)
		return err
	}

	modelDBTableNameMap := make(map[string]struct{}, 0)
	for _, name := range allDBTables {
		if common.IsObjectShardingTable(name) {
			modelDBTableNameMap[name] = struct{}{}
		}
	}

	objs := make([]metadata.Object, 0)
	if err := dt.db.Table(common.BKTableNameObjDes).Find(nil).Fields(common.BKObjIDField,
		common.BKIsPre).All(ctx, &objs); err != nil {
		blog.Errorf("get all common object id  error. err: %s, rid: %s", err.Error(), dt.rid)
		return err
	}

	for _, obj := range objs {
		blog.Infof("start object(%s) sharding table rid: %s", obj.ObjectID, dt.rid)
		instTable := common.GetObjectInstTableName(obj.ObjectID)
		instAsstTable := common.GetObjectInstAsstTableName(obj.ObjectID)

		hasUniqueIndex := true
		uniques, err := dt.findObjUniques(ctx, obj.ObjectID)
		if err != nil {
			blog.Errorf("object(%s) logic unique to db index error. err: %s, rid: %s",
				obj.ObjectID, err.Error(), dt.rid)
			// 服务降级，只是不处理唯一索引
			hasUniqueIndex = false
		}

		// 是否需要执行db表中索引和定义的索引同步操作
		canObjIndex := true
		objIndexes := append(index.InstanceIndexes(), uniques...)
		// 内置模型不需要简表
		if !obj.IsPre {
			// 判断模型实例表是否存在, 不存在新建
			if _, exist := modelDBTableNameMap[instTable]; !exist {
				if err := dt.db.CreateTable(ctx, instTable); err != nil {
					// TODO: 需要报警，但是不影响后需逻辑继续执行下一个
					blog.Errorf("create table(%s) error. err: %s, rid: %s", instTable, err.Error(), dt.rid)
					// NOTICE: 索引有专门的任务处理
				}
				dt.createIndexes(ctx, instTable, objIndexes)
				canObjIndex = false
			}
		}

		if canObjIndex {
			if err := dt.syncObjTableIndexes(ctx, instTable, objIndexes, nil, hasUniqueIndex); err != nil {
				blog.Errorf("sync table(%s) definition index to table error. err: %s, rid: %s",
					instTable, err.Error(), dt.rid)
			}
		}

		// 判断模型实例关联关系表是否存在， 不存在新建
		if _, exist := modelDBTableNameMap[instAsstTable]; !exist {
			if err := dt.db.CreateTable(ctx, instAsstTable); err != nil {
				// TODO: 需要报警，但是不影响后需逻辑继续执行下一个
				blog.Errorf("create table(%s) error. err: %s, rid: %s", instAsstTable, err.Error(), dt.rid)
				// NOTICE: 索引有专门的任务处理
			}
			dt.createIndexes(ctx, instAsstTable, index.InstanceAssociationIndexes())
		} else {
			if err := dt.syncIndexesToDB(ctx, instAsstTable, index.InstanceAssociationIndexes(), nil); err != nil {
				blog.Errorf("sync table(%s) definition index to table error. err: %s, rid: %s",
					instAsstTable, err.Error(), dt.rid)
			}

		}

		delete(modelDBTableNameMap, instTable)
		delete(modelDBTableNameMap, instAsstTable)
	}

	if err := dt.cleanRedundancyTable(ctx, modelDBTableNameMap); err != nil {
		// TODO: 需要报警，但是不影响后需逻辑继续执行下一个
		blog.Errorf("clean redundancy table Name map:(%#v) error. err: %s, rid: %s",
			modelDBTableNameMap, err.Error(), dt.rid)
	}
	return nil
}

func (dt *dbTable) cleanRedundancyTable(ctx context.Context, modelDBTableNameMap map[string]struct{}) error {
	filter := map[string]interface{}{common.BKIsPre: false}
	objIDInterfaceArr, err := dt.db.Table(common.BKTableNameObjDes).Distinct(ctx, common.BKObjIDField, filter)
	if err != nil {
		blog.Errorf("get all common object id  error. err: %s, rid: %s", err.Error(), dt.rid)
		// NOTICE: 错误直接忽略不行后需功能
		return err
	}
	// 再次确认数据，保证存在模型的的表不被删除
	for _, objIDInterface := range objIDInterfaceArr {
		strObjID := fmt.Sprintf("%v", objIDInterface)
		instTable := common.GetObjectInstTableName(strObjID)
		instAsstTable := common.GetObjectInstAsstTableName(strObjID)
		delete(modelDBTableNameMap, instTable)
		delete(modelDBTableNameMap, instAsstTable)

	}
	for name := range modelDBTableNameMap {
		row := make(map[string]interface{}, 0)
		// 检查是否有数据
		if err := dt.db.Table(name).Find(nil).One(ctx, &row); err != nil {
			if dt.db.IsNotFoundError(err) {
				blog.Infof("delete sharding table(%s) rid: %s", name, dt.rid)
				// 没有数据删除
				if err := dt.db.DropTable(ctx, name); err != nil {
					// TODO: 需要报警，但是不影响后需逻辑继续执行下一个
					blog.Errorf("delete table(%s) error. err: %s, rid: %s", name, err.Error(), dt.rid)
					continue
				}
			} else {
				// TODO: 需要报警，但是不影响后需逻辑继续执行下一个
				blog.Errorf("find table(%s) one row error. err: %s, rid: %s", name, err.Error(), dt.rid)

			}

		} else {
			// TODO: 需要报警，但是不影响后需逻辑继续执行下一个
			blog.Errorf("can't drop the non-empty sharding table, table name: %s, rid: %d", name, dt.rid)
		}

	}

	return nil
}

func (dt *dbTable) createIndexes(ctx context.Context, tableName string, indexes []types.Index) {

	for _, index := range indexes {
		if err := dt.db.Table(tableName).CreateIndex(ctx, index); err != nil {
			// 不影响后需执行，
			blog.WarnJSON("create table(%s) error. index: %s, err: %s, rid: %s", tableName, index, err, dt.rid)
		}
	}

	return
}

func (dt *dbTable) findObjUniques(ctx context.Context, objID string) ([]types.Index, error) {

	filter := map[string]interface{}{
		common.BKObjIDField: objID,
	}
	uniqueIdxs := make([]metadata.ObjectUnique, 0)
	if err := dt.db.Table(common.BKTableNameObjUnique).Find(filter).All(ctx, &uniqueIdxs); err != nil {
		newErr := fmt.Errorf("get obj(%s) logic unique index error. err: %s", objID, err.Error())
		blog.ErrorJSON("%s, rid: %s", newErr.Error(), dt.rid)
		return nil, newErr
	}

	// 返回的数据只有common.BKPropertyIDField, common.BKPropertyTypeField, common.BKFieldID 三个字段
	attrs, err := dt.findObjAttrs(ctx, objID)
	if err != nil {
		return nil, err
	}

	var indexes []types.Index
	for _, idx := range uniqueIdxs {
		newDBIndex, err := index.ToDBUniqueIndex(objID, idx.ID, idx.Keys, attrs)
		if err != nil {
			newErr := fmt.Errorf("obj(%s). %s", objID, err.Error())
			blog.ErrorJSON("%s, rid: %s", newErr.Error(), dt.rid)
			return nil, newErr
		}
		indexes = append(indexes, newDBIndex)

	}

	return indexes, nil
}

func ErrDropIndexNameNotFound(err error) bool {
	if strings.HasPrefix(err.Error(), "index not found with name") {
		return true
	}
	return false
}