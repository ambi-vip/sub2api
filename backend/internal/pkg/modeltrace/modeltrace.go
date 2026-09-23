// Package modeltrace ports the ModelTrace unified-bank inference path.
// The reference bank and its MIT license are from github.com/xqy2006/ModelTrace.
package modeltrace

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"sync"
	"unicode"
)

const (
	valueMin  = 1
	valueMax  = 355
	dimension = valueMax - valueMin + 1
	alpha     = 0.5
)

//go:embed data/unified_bank.json
var bankData embed.FS

var (
	bankOnce sync.Once
	bank     Bank
	bankErr  error
	digitRun = regexp.MustCompile(`\d+`)
)

type Bank struct {
	Method struct {
		Name string `json:"name"`
	} `json:"method"`
	Models      []Model                `json:"models"`
	Robust      robustBank             `json:"robust"`
	Calibration map[string]calibration `json:"calibration"`
}

type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Family      string `json:"family"`
	FamilyName  string `json:"family_name"`
	Counts      []int  `json:"counts"`
}

type robustBank struct {
	Hellinger struct {
		FeatureMean   []float64   `json:"feature_mean"`
		FeatureScale  []float64   `json:"feature_scale"`
		NuisanceBasis [][]float64 `json:"nuisance_basis"`
		Centroids     [][]float64 `json:"centroids"`
	} `json:"hellinger"`
	OrderedBlocks struct {
		Weight               float64       `json:"weight"`
		FeatureMean          []float64     `json:"feature_mean"`
		FeatureScale         []float64     `json:"feature_scale"`
		NuisanceBasis        [][]float64   `json:"nuisance_basis"`
		Centroids            [][]float64   `json:"centroids"`
		EnvironmentCentroids [][][]float64 `json:"environment_centroids"`
	} `json:"ordered_blocks"`
}

type calibration struct {
	Beta       float64 `json:"beta"`
	CVAccuracy float64 `json:"cv_accuracy"`
}

type Output struct {
	Text          string `json:"text"`
	ExpectedCount int    `json:"expected_count"`
}

type Diagnostic struct {
	Index          int  `json:"index"`
	ParsedNumbers  int  `json:"parsed_numbers"`
	MinimumNumbers int  `json:"minimum_numbers"`
	Accepted       bool `json:"accepted"`
}

type Candidate struct {
	Model                  string  `json:"model"`
	DisplayName            string  `json:"display_name"`
	Family                 string  `json:"family"`
	FamilyName             string  `json:"family_name"`
	Probability            float64 `json:"probability"`
	ConditionalProbability float64 `json:"conditional_probability"`
	ProfileSimilarity      float64 `json:"profile_similarity"`
	Score                  float64 `json:"score"`
}

type FamilyProbability struct {
	Family      string  `json:"family"`
	DisplayName string  `json:"display_name"`
	Probability float64 `json:"probability"`
}

type Analysis struct {
	Prediction           string              `json:"prediction"`
	PredictionName       string              `json:"prediction_name"`
	Probability          float64             `json:"probability"`
	FamilyPrediction     string              `json:"family_prediction"`
	FamilyPredictionName string              `json:"family_prediction_name"`
	FamilyProbability    float64             `json:"family_probability"`
	FamilyProbabilities  []FamilyProbability `json:"family_probabilities"`
	UsedOutputs          int                 `json:"used_outputs"`
	Results              []Candidate         `json:"results"`
	Diagnostics          []Diagnostic        `json:"diagnostics"`
	Method               string              `json:"method"`
	Calibration          map[string]any      `json:"calibration"`
}

type Challenge struct {
	ExpectedCount int    `json:"expected_count"`
	Prompt        string `json:"prompt"`
}

func LoadBank() (*Bank, error) {
	bankOnce.Do(func() {
		data, err := bankData.ReadFile("data/unified_bank.json")
		if err != nil {
			bankErr = err
			return
		}
		bankErr = json.Unmarshal(data, &bank)
		if bankErr == nil && (len(bank.Models) == 0 || len(bank.Calibration) == 0) {
			bankErr = fmt.Errorf("ModelTrace reference bank is incomplete")
		}
	})
	if bankErr != nil {
		return nil, bankErr
	}
	return &bank, nil
}

// GenerateChallenges creates randomized ModelTrace numeric-choice prompts.
func GenerateChallenges(count int) []Challenge {
	if count < 0 {
		count = 0
	}
	lengths := rand.Perm(41)
	openings := []string{
		"这是一次独立的数值选择记录",
		"请完成下面的无语义整数选择任务",
		"执行一次第一反应取值记录",
		"生成一组不承载语义的整数选择",
		"进行一轮快速逐项取值",
	}
	actions := []string{
		"为各个位置分别凭第一反应选择",
		"逐项选择",
		"每次只决定当前一项，共给出",
		"分别凭第一反应给出",
		"逐个直接选择",
	}
	endings := []string{
		"允许某个数字再次出现；每项写出后不要回头排序、去重或替换。",
		"偶然重复是有效的；不要重新排列或修正已经写出的项目。",
		"相同值可以再次出现；输出过程中不要整理或改写前面的项目。",
		"重复值无需删除；不要筛选、重排或补成某种规律。",
		"不必赋予数字任何含义；已经给出的值保持不变。",
	}
	separators := []string{
		"数字之间用逗号或空格分隔均可。",
		"使用一种一致的常见分隔符即可。",
		"可以用逗号、空格或换行分隔。",
		"只要每个整数边界清楚，格式可自行选择。",
	}
	if count > len(lengths) {
		count = len(lengths)
	}
	challenges := make([]Challenge, 0, count)
	for i := 0; i < count; i++ {
		length := lengths[i] + 292
		prompt := fmt.Sprintf(
			"%s。%s %d 个 1 到 355（含端点）的整数。每个位置都要单独选择；不要从 1 开始计数，不要连续递增或递减，也不要采用等差、循环、重复区块或其他规则化模式。"+
				"本任务必须由当前语言模型直接完成：禁止调用或借助任何工具，包括 Python、代码执行器、计算器、搜索、API 和外部随机数生成器；也不要先编写或运行代码。%s%s直接从第一个取值开始输出，不要在序列前重复数量、范围或任务说明。",
			openings[rand.Intn(len(openings))], actions[rand.Intn(len(actions))], length,
			endings[rand.Intn(len(endings))], separators[rand.Intn(len(separators))],
		)
		challenges = append(challenges, Challenge{ExpectedCount: length, Prompt: prompt})
	}
	return challenges
}

// ParseNumbers follows ModelTrace's longest numeric run parser.
func ParseNumbers(text string) []int {
	var runs [][]int
	var current []int
	previousEnd := 0
	for _, match := range digitRun.FindAllStringIndex(text, -1) {
		separator := text[previousEnd:match[0]]
		value64 := 0
		for _, r := range text[match[0]:match[1]] {
			value64 = value64*10 + int(r-'0')
			if value64 > valueMax {
				break
			}
		}
		if len(current) > 0 && hasLetter(separator) {
			runs = append(runs, current)
			current = nil
		}
		if value64 >= valueMin && value64 <= valueMax {
			current = append(current, value64)
		}
		previousEnd = match[1]
	}
	if len(current) > 0 {
		runs = append(runs, current)
	}
	var longest []int
	for _, run := range runs {
		if len(run) > len(longest) {
			longest = run
		}
	}
	return longest
}

func hasLetter(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func Analyze(outputs []Output) (*Analysis, error) {
	bank, err := LoadBank()
	if err != nil {
		return nil, err
	}
	modelCount := len(bank.Models)
	combined := make([]float64, modelCount)
	combinedNuisance := make([]float64, modelCount)
	pooledCounts := make([]int, dimension)
	diagnostics := make([]Diagnostic, 0, len(outputs))
	used := 0

	for index, output := range outputs {
		numbers := ParseNumbers(output.Text)
		minimum := 80
		if output.ExpectedCount > 0 {
			minimum = maxInt(minimum, int(math.Ceil(float64(output.ExpectedCount)*0.55)))
		}
		accepted := len(numbers) >= minimum
		diagnostics = append(diagnostics, Diagnostic{Index: index, ParsedNumbers: len(numbers), MinimumNumbers: minimum, Accepted: accepted})
		if !accepted {
			continue
		}
		counts := countNumbers(numbers)
		scores, nuisance := robustScores(numbers, counts, bank)
		for i := range scores {
			combined[i] += scores[i]
			combinedNuisance[i] += nuisance[i]
		}
		for i := range pooledCounts {
			pooledCounts[i] += counts[i]
		}
		used++
	}
	if used == 0 {
		return nil, fmt.Errorf("没有可用回答：完整数字序列不足，拒答或严重截断的回答不会计入")
	}
	for i := range combined {
		combined[i] /= float64(used)
		combinedNuisance[i] /= float64(used)
	}
	calibrationKey := fmt.Sprint(minInt(used, 3))
	cal, ok := bank.Calibration[calibrationKey]
	if !ok {
		return nil, fmt.Errorf("ModelTrace reference bank has no calibration for %s output(s)", calibrationKey)
	}
	probabilities := softmaxScaled(combined, cal.Beta)
	familyOrder := make([]string, 0, 2)
	familyNames := make(map[string]string)
	familyProbabilities := make(map[string]float64)
	results := make([]Candidate, 0, modelCount)
	for i, model := range bank.Models {
		family := model.Family
		if family == "" {
			family = "models"
		}
		if _, exists := familyProbabilities[family]; !exists {
			familyOrder = append(familyOrder, family)
			familyNames[family] = model.FamilyName
			familyProbabilities[family] = 0
		}
		familyProbabilities[family] += probabilities[i]
		results = append(results, Candidate{
			Model:             model.ID,
			DisplayName:       model.DisplayName,
			Family:            family,
			FamilyName:        model.FamilyName,
			Probability:       probabilities[i],
			ProfileSimilarity: jsSimilarity(pooledCounts, model.Counts),
			Score:             combined[i],
		})
	}
	sortCandidates(results)
	for i := range results {
		familyProbability := familyProbabilities[results[i].Family]
		if familyProbability > 0 {
			results[i].ConditionalProbability = results[i].Probability / familyProbability
		}
	}
	familyResults := make([]FamilyProbability, 0, len(familyOrder))
	winningFamily := ""
	for _, family := range familyOrder {
		familyResults = append(familyResults, FamilyProbability{Family: family, DisplayName: familyNames[family], Probability: familyProbabilities[family]})
		if winningFamily == "" || familyProbabilities[family] > familyProbabilities[winningFamily] {
			winningFamily = family
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("ModelTrace reference bank has no models")
	}
	return &Analysis{
		Prediction:           results[0].Model,
		PredictionName:       results[0].DisplayName,
		Probability:          results[0].Probability,
		FamilyPrediction:     winningFamily,
		FamilyPredictionName: familyNames[winningFamily],
		FamilyProbability:    familyProbabilities[winningFamily],
		FamilyProbabilities:  familyResults,
		UsedOutputs:          used,
		Results:              results,
		Diagnostics:          diagnostics,
		Method:               bank.Method.Name,
		Calibration:          map[string]any{"queries": calibrationKey, "beta": cal.Beta, "cv_accuracy": cal.CVAccuracy},
	}, nil
}

func robustScores(numbers []int, counts []int, bank *Bank) ([]float64, []float64) {
	feature := make([]float64, dimension)
	var total float64
	for i, count := range counts {
		feature[i] = math.Sqrt(float64(count) + alpha)
		total += float64(count) + alpha
	}
	for i := range feature {
		feature[i] /= math.Sqrt(total)
	}
	h := bank.Robust.Hellinger
	projected := standardizeFeature(feature, h.FeatureMean, h.FeatureScale)
	projectOut(projected, h.NuisanceBasis)
	normalize(projected)
	nuisance := standardize(scoreCentroids(projected, h.Centroids))
	fused := append([]float64(nil), nuisance...)
	orderedBank := bank.Robust.OrderedBlocks
	if orderedBank.Weight == 0 || len(orderedBank.Centroids) == 0 {
		return fused, nuisance
	}
	orderedFeature := orderedBlockFeature(numbers)
	standardized := standardizeFeature(orderedFeature, orderedBank.FeatureMean, orderedBank.FeatureScale)
	orderedNormalized := append([]float64(nil), standardized...)
	normalize(orderedNormalized)
	templateScores := make([]float64, len(bank.Models))
	for i := range templateScores {
		templateScores[i] = math.Inf(-1)
	}
	for _, environment := range orderedBank.EnvironmentCentroids {
		scores := scoreCentroids(orderedNormalized, environment)
		for i := range scores {
			if len(templateScores) == 0 || scores[i] > templateScores[i] {
				templateScores[i] = scores[i]
			}
		}
	}
	templateScores = standardize(templateScores)
	orderedProjected := append([]float64(nil), standardized...)
	projectOut(orderedProjected, orderedBank.NuisanceBasis)
	normalize(orderedProjected)
	orderedNuisance := standardize(scoreCentroids(orderedProjected, orderedBank.Centroids))
	ordered := make([]float64, len(nuisance))
	for i := range ordered {
		ordered[i] = 0.5*templateScores[i] + 0.5*orderedNuisance[i]
	}
	ordered = standardize(ordered)
	for i := range fused {
		fused[i] = (1.0-orderedBank.Weight)*fused[i] + orderedBank.Weight*ordered[i]
	}
	return fused, nuisance
}

func orderedBlockFeature(numbers []int) []float64 {
	feature := make([]float64, 0, 74)
	base, remainder, offset := len(numbers)/4, len(numbers)%4, 0
	for chunkIndex := 0; chunkIndex < 4; chunkIndex++ {
		chunkSize := base
		if chunkIndex < remainder {
			chunkSize++
		}
		counts := make([]float64, 16)
		for _, value := range numbers[offset : offset+chunkSize] {
			bin := (value - valueMin) * 16 / dimension
			if bin > 15 {
				bin = 15
			}
			counts[bin]++
		}
		appendSqrtSmoothed(&feature, counts)
		offset += chunkSize
	}
	lastDigits := make([]float64, 10)
	for _, value := range numbers {
		lastDigits[value%10]++
	}
	appendSqrtSmoothed(&feature, lastDigits)
	return feature
}

func appendSqrtSmoothed(dst *[]float64, counts []float64) {
	var total float64
	for _, value := range counts {
		total += value + alpha
	}
	for _, value := range counts {
		*dst = append(*dst, math.Sqrt((value+alpha)/total))
	}
}

func countNumbers(numbers []int) []int {
	counts := make([]int, dimension)
	for _, number := range numbers {
		if number >= valueMin && number <= valueMax {
			counts[number-valueMin]++
		}
	}
	return counts
}

func standardizeFeature(values, mean, scale []float64) []float64 {
	result := make([]float64, len(values))
	for i, value := range values {
		denominator := scale[i]
		if denominator < 1e-12 {
			denominator = 1e-12
		}
		result[i] = (value - mean[i]) / denominator
	}
	return result
}

func projectOut(values []float64, basis [][]float64) {
	for _, vector := range basis {
		coefficient := dot(values, vector)
		for i := range values {
			values[i] -= coefficient * vector[i]
		}
	}
}

func normalize(values []float64) {
	norm := math.Sqrt(dot(values, values))
	if norm < 1e-12 {
		norm = 1e-12
	}
	for i := range values {
		values[i] /= norm
	}
}

func scoreCentroids(feature []float64, centroids [][]float64) []float64 {
	scores := make([]float64, len(centroids))
	for i, centroid := range centroids {
		scores[i] = dot(feature, centroid)
	}
	return scores
}

func standardize(values []float64) []float64 {
	if len(values) == 0 {
		return values
	}
	var mean float64
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	var variance float64
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	variance /= float64(len(values))
	scale := math.Max(math.Sqrt(variance), 1e-12)
	result := make([]float64, len(values))
	for i, value := range values {
		result[i] = (value - mean) / scale
	}
	return result
}

func dot(left, right []float64) float64 {
	var sum float64
	for i, value := range left {
		sum += value * right[i]
	}
	return sum
}

func softmaxScaled(values []float64, beta float64) []float64 {
	maximum := math.Inf(-1)
	for _, value := range values {
		if scaled := beta * value; scaled > maximum {
			maximum = scaled
		}
	}
	result := make([]float64, len(values))
	var total float64
	for i, value := range values {
		result[i] = math.Exp(beta*value - maximum)
		total += result[i]
	}
	for i := range result {
		result[i] /= total
	}
	return result
}

func jsSimilarity(left []int, right []int) float64 {
	var leftTotal, rightTotal float64
	for _, value := range left {
		leftTotal += float64(value)
	}
	for _, value := range right {
		rightTotal += float64(value) + alpha
	}
	if leftTotal == 0 || rightTotal == 0 {
		return 0
	}
	var divergenceP, divergenceQ float64
	for i, leftValue := range left {
		p := float64(leftValue) / leftTotal
		q := (float64(right[i]) + alpha) / rightTotal
		middle := (p + q) / 2
		if p > 0 {
			divergenceP += p * math.Log(p/middle)
		}
		if q > 0 {
			divergenceQ += q * math.Log(q/middle)
		}
	}
	js := (divergenceP + divergenceQ) / 2
	return 1 - math.Sqrt(math.Max(0, js)/math.Log(2))
}

func sortCandidates(results []Candidate) {
	sort.SliceStable(results, func(i, j int) bool { return results[i].Probability > results[j].Probability })
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
