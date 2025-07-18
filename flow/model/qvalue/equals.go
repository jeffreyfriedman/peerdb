package qvalue

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	geom "github.com/twpayne/go-geos"

	"github.com/PeerDB-io/peerdb/flow/shared/datatypes"
	"github.com/PeerDB-io/peerdb/flow/shared/types"
)

func valueEmpty(value any) bool {
	return value == nil || value == "" || value == "null" ||
		(reflect.TypeOf(value).Kind() == reflect.Slice && reflect.ValueOf(value).Len() == 0)
}

func Equals(qv types.QValue, other types.QValue) bool {
	qvValue := qv.Value()
	otherValue := other.Value()
	if valueEmpty(qvValue) && valueEmpty(otherValue) {
		return true
	}

	switch q := qv.(type) {
	case types.QValueInvalid:
		return true
	case types.QValueFloat32:
		float2, ok2 := getFloat32(other.Value())
		return ok2 && q.Val == float2
	case types.QValueFloat64:
		float2, ok2 := getFloat64(other.Value())
		return ok2 && q.Val == float2
	case types.QValueInt8:
		int2, ok2 := getInt64(other.Value())
		return ok2 && int64(q.Val) == int2
	case types.QValueInt16:
		int2, ok2 := getInt64(other.Value())
		return ok2 && int64(q.Val) == int2
	case types.QValueInt32:
		int2, ok2 := getInt64(other.Value())
		return ok2 && int64(q.Val) == int2
	case types.QValueInt64:
		int2, ok2 := getInt64(other.Value())
		return ok2 && q.Val == int2
	case types.QValueUInt8:
		int2, ok2 := getUInt64(other.Value())
		return ok2 && uint64(q.Val) == int2
	case types.QValueUInt16:
		int2, ok2 := getUInt64(other.Value())
		return ok2 && uint64(q.Val) == int2
	case types.QValueUInt32:
		int2, ok2 := getUInt64(other.Value())
		return ok2 && uint64(q.Val) == int2
	case types.QValueUInt64:
		int2, ok2 := getUInt64(other.Value())
		return ok2 && q.Val == int2
	case types.QValueBoolean:
		if otherVal, ok := other.(types.QValueBoolean); ok {
			return q.Val == otherVal.Val
		}
		return false
	case types.QValueQChar:
		if otherVal, ok := other.(types.QValueQChar); ok {
			return q.Val == otherVal.Val
		}
		return false
	case types.QValueString:
		return compareString(q.Val, otherValue)
	case types.QValueEnum:
		return compareString(q.Val, otherValue)
	case types.QValueINET:
		return compareString(q.Val, otherValue)
	case types.QValueCIDR:
		return compareString(q.Val, otherValue)
	case types.QValueMacaddr:
		return compareString(q.Val, otherValue)
	// all internally represented as a Golang time.Time
	case types.QValueTimestamp, types.QValueTimestampTZ:
		return compareGoTimestamp(qvValue, otherValue)
	case types.QValueTime, types.QValueTimeTZ:
		return compareGoTime(qvValue, otherValue)
	case types.QValueDate:
		return compareGoDate(qvValue, otherValue)
	case types.QValueNumeric:
		return compareNumeric(q.Val, otherValue)
	case types.QValueBytes:
		return compareBytes(qvValue, otherValue)
	case types.QValueUUID:
		return compareUUID(qvValue, otherValue)
	case types.QValueJSON:
		if otherValue == nil || otherValue == "" {
			// TODO make this more strict
			return true
		}
		var a any
		var b any
		if err := json.Unmarshal([]byte(q.Val), &a); err != nil {
			return false
		}
		if err := json.Unmarshal([]byte(otherValue.(string)), &b); err != nil {
			return false
		}
		return reflect.DeepEqual(a, b)
	case types.QValueGeometry:
		return compareGeometry(q.Val, otherValue)
	case types.QValueGeography:
		return compareGeometry(q.Val, otherValue)
	case types.QValueHStore:
		return compareHStore(q.Val, otherValue)
	case types.QValueArrayInt32, types.QValueArrayInt16, types.QValueArrayInt64, types.QValueArrayFloat32, types.QValueArrayFloat64:
		return compareNativeNumericArrays(qvValue, otherValue)
	case types.QValueArrayNumeric:
		return compareNumericArrays(q.Val, otherValue)
	case types.QValueArrayDate:
		return compareDateArrays(q.Val, otherValue)
	case types.QValueArrayInterval:
		return compareArrays(q.Val, otherValue)
	case types.QValueArrayTimestamp:
		return compareTimeArrays(q.Val, otherValue)
	case types.QValueArrayTimestampTZ:
		return compareTimeArrays(q.Val, otherValue)
	case types.QValueArrayBoolean:
		return compareArrays(q.Val, otherValue)
	case types.QValueArrayUUID:
		return compareArrays(q.Val, otherValue)
	case types.QValueArrayString:
		if qjson, ok := other.(types.QValueJSON); ok {
			var val []string
			if err := json.Unmarshal([]byte(qjson.Val), &val); err != nil {
				return false
			}
			otherValue = val
		}

		return compareArrays(q.Val, otherValue)
	case types.QValueArrayEnum:
		if qjson, ok := other.(types.QValueJSON); ok {
			var val []string
			if err := json.Unmarshal([]byte(qjson.Val), &val); err != nil {
				return false
			}
			otherValue = val
		}

		return compareArrays(q.Val, otherValue)
	default:
		return false
	}
}

func compareString(s1 string, value2 any) bool {
	s2, ok := value2.(string)
	return ok && s1 == s2
}

func compareGoTimestamp(value1, value2 any) bool {
	et1, ok1 := value1.(time.Time)
	et2, ok2 := value2.(time.Time)

	if !ok1 || !ok2 {
		return false
	}

	return et1.UnixMicro() == et2.UnixMicro()
}

func compareGoTime(value1, value2 any) bool {
	t1, ok1 := value1.(time.Duration)
	t2, ok2 := value2.(time.Duration)

	if !ok1 {
		var tm time.Time
		tm, ok1 = value1.(time.Time)
		t1 = tm.Sub(time.Unix(0, 0).UTC())
	}
	if !ok2 {
		var tm time.Time
		tm, ok2 = value2.(time.Time)
		t2 = tm.Sub(time.Unix(0, 0).UTC())
	}

	return ok1 && ok2 && t1 == t2
}

func compareGoDate(value1, value2 any) bool {
	t1, ok1 := value1.(time.Time)
	t2, ok2 := value2.(time.Time)

	if !ok1 || !ok2 {
		return false
	}

	y1, m1, d1 := t1.Date()
	y2, m2, d2 := t2.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func compareUUID(value1, value2 any) bool {
	uuid1, ok1 := getUUID(value1)
	uuid2, ok2 := getUUID(value2)

	return ok1 && ok2 && uuid1 == uuid2
}

func compareBytes(value1, value2 any) bool {
	bytes1, ok1 := getBytes(value1)
	bytes2, ok2 := getBytes(value2)

	return ok1 && ok2 && bytes.Equal(bytes1, bytes2)
}

func compareNumeric(value1, value2 any) bool {
	num1, ok1 := getDecimal(value1)
	num2, ok2 := getDecimal(value2)

	if !ok1 || !ok2 {
		return false
	}

	return num1.Equal(num2)
}

func compareHStore(str1 string, value2 any) bool {
	str2 := value2.(string)
	if str1 == str2 {
		return true
	}
	parsedHStore1, err := datatypes.ParseHstore(str1)
	if err != nil {
		panic(err)
	}
	return parsedHStore1 == strings.ReplaceAll(strings.ReplaceAll(str2, " ", ""), "\n", "")
}

func compareGeometry(geoWkt string, value2 any) bool {
	geo2, err := geom.NewGeomFromWKT(value2.(string))
	if err != nil {
		panic(err)
	}

	if strings.HasPrefix(geoWkt, "SRID=") {
		_, wkt, found := strings.Cut(geoWkt, ";")
		if found {
			geoWkt = wkt
		}
	}

	geo1, err := geom.NewGeomFromWKT(geoWkt)
	if err != nil {
		panic(err)
	}
	return geo1.Equals(geo2)
}

func convertNativeNumericArrayToFloat64Array(val any) []float64 {
	switch v := val.(type) {
	case []int16:
		result := make([]float64, len(v))
		for i, value := range v {
			result[i] = float64(value)
		}
		return result
	case []int32:
		result := make([]float64, len(v))
		for i, value := range v {
			result[i] = float64(value)
		}
		return result
	case []int64:
		result := make([]float64, len(v))
		for i, value := range v {
			result[i] = float64(value)
		}
		return result
	case []float32:
		result := make([]float64, len(v))
		for i, value := range v {
			result[i] = float64(value)
		}
		return result
	case []float64:
		return v
	case string:
		var val []float64
		if err := json.Unmarshal([]byte(v), &val); err != nil {
			return nil
		}
		return val
	default:
		return nil
	}
}

func compareNativeNumericArrays(value1, value2 any) bool {
	array1 := convertNativeNumericArrayToFloat64Array(value1)
	array2 := convertNativeNumericArrayToFloat64Array(value2)
	if array1 == nil || array2 == nil {
		return false
	}

	return slices.EqualFunc(array1, array2, func(x float64, y float64) bool {
		return math.Abs(x-y) < 1e9
	})
}

func compareNumericArrays(array1 []decimal.Decimal, value2 any) bool {
	array2, ok2 := value2.([]decimal.Decimal)
	return ok2 && slices.EqualFunc(array1, array2, func(x, y decimal.Decimal) bool {
		return x.Equal(y)
	})
}

func compareDateArrays(array1 []time.Time, value2 any) bool {
	array2, ok2 := value2.([]time.Time)
	return ok2 && slices.EqualFunc(array1, array2, func(x time.Time, y time.Time) bool {
		return x.Year() == y.Year() && x.Month() == y.Month() && x.Day() == y.Day()
	})
}

func compareTimeArrays(array1 []time.Time, value2 any) bool {
	array2, ok2 := value2.([]time.Time)
	return ok2 && slices.EqualFunc(array1, array2, func(x time.Time, y time.Time) bool {
		return x.UnixMicro() == y.UnixMicro()
	})
}

func compareArrays[T comparable](array1 []T, value2 any) bool {
	array2, ok2 := value2.([]T)
	return ok2 && slices.Equal(array1, array2)
}

func getUInt64(v any) (uint64, bool) {
	switch value := v.(type) {
	case uint8:
		return uint64(value), true
	case uint16:
		return uint64(value), true
	case uint32:
		return uint64(value), true
	case uint64:
		return value, true
	case decimal.Decimal:
		return value.BigInt().Uint64(), true
	case string:
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func getInt64(v any) (int64, bool) {
	switch value := v.(type) {
	case int8:
		return int64(value), true
	case int16:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case decimal.Decimal:
		return value.IntPart(), true
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func getFloat32(v any) (float32, bool) {
	switch value := v.(type) {
	case float32:
		return value, true
	case float64:
		return float32(value), true
	case string:
		parsed, err := strconv.ParseFloat(value, 32)
		if err == nil {
			return float32(parsed), true
		}
	}
	return 0, false
}

func getFloat64(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func getBytes(v any) ([]byte, bool) {
	switch value := v.(type) {
	case []byte:
		return value, true
	case string:
		return []byte(value), true
	case nil:
		return nil, true
	default:
		return nil, false
	}
}

func getUUID(v any) (uuid.UUID, bool) {
	switch value := v.(type) {
	case uuid.UUID:
		return value, true
	case string:
		parsed, err := uuid.Parse(value)
		if err == nil {
			return parsed, true
		}
	case [16]byte:
		return uuid.UUID(value), true
	}

	return uuid.UUID{}, false
}

// getDecimal attempts to parse a decimal from an interface
func getDecimal(v any) (decimal.Decimal, bool) {
	switch value := v.(type) {
	case decimal.Decimal:
		return value, true
	case string:
		parsed, err := decimal.NewFromString(value)
		if err != nil {
			panic(err)
		}
		return parsed, true
	case float64:
		return decimal.NewFromFloat(value), true
	case int64:
		return decimal.NewFromInt(value), true
	case uint64:
		return decimal.NewFromUint64(value), true
	case float32:
		return decimal.NewFromFloat32(value), true
	case int32:
		return decimal.NewFromInt(int64(value)), true
	case uint32:
		return decimal.NewFromInt(int64(value)), true
	case int:
		return decimal.NewFromInt(int64(value)), true
	case uint:
		return decimal.NewFromInt(int64(value)), true
	case int8:
		return decimal.NewFromInt(int64(value)), true
	case uint8:
		return decimal.NewFromInt(int64(value)), true
	case int16:
		return decimal.NewFromInt(int64(value)), true
	case uint16:
		return decimal.NewFromInt(int64(value)), true
	}
	return decimal.Decimal{}, false
}
