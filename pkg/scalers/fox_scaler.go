package scalers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-logr/logr"
	v2 "k8s.io/api/autoscaling/v2"
	"k8s.io/metrics/pkg/apis/external_metrics"

	"github.com/kedacore/keda/v2/pkg/scalers/scalersconfig"
	kedautil "github.com/kedacore/keda/v2/pkg/util"
)

type foxScaler struct {
	metricType v2.MetricTargetType
	metadata   *foxMetadata
	httpClient *http.Client
	logger     logr.Logger
}

type foxMetadata struct {
	host                       string
	targetQueryValue           float64
	activationTargetQueryValue float64
	triggerIndex               int

	// Authentication
	username string
	password string
}

type foxResponse struct {
	Response struct {
		Result int `json:"result"`
	} `json:"response"`
}

// NewSolrScaler creates a new solr Scaler
func NewFoxScaler(config *scalersconfig.ScalerConfig) (Scaler, error) {
	metricType, err := GetMetricTargetType(config)
	if err != nil {
		return nil, fmt.Errorf("error getting scaler metric type: %w", err)
	}

	meta, err := parseFoxMetadata(config)
	if err != nil {
		return nil, fmt.Errorf("error parsing Fox metadata: %w", err)
	}
	httpClient := kedautil.CreateHTTPClient(config.GlobalHTTPTimeout, false)

	logger := InitializeLogger(config, "fox_scaler")

	return &foxScaler{
		metricType: metricType,
		metadata:   meta,
		httpClient: httpClient,
		logger:     logger,
	}, nil
}

// parseSolrMetadata parses the metadata and returns a solrMetadata or an error if the ScalerConfig is invalid.
func parseFoxMetadata(config *scalersconfig.ScalerConfig) (*foxMetadata, error) {
	meta := foxMetadata{}

	if val, ok := config.TriggerMetadata["host"]; ok {
		meta.host = val
	} else {
		return nil, fmt.Errorf("no host given")
	}

	if val, ok := config.TriggerMetadata["targetQueryValue"]; ok {
		targetQueryValue, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return nil, fmt.Errorf("targetQueryValue parsing error %w", err)
		}
		meta.targetQueryValue = targetQueryValue
	} else {
		if config.AsMetricSource {
			meta.targetQueryValue = 0
		} else {
			return nil, fmt.Errorf("no targetQueryValue given")
		}
	}

	meta.activationTargetQueryValue = 0
	if val, ok := config.TriggerMetadata["activationTargetQueryValue"]; ok {
		activationTargetQueryValue, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid activationTargetQueryValue - must be an integer")
		}
		meta.activationTargetQueryValue = activationTargetQueryValue
	}
	// Parse Authentication
	if val, ok := config.AuthParams["username"]; ok {
		meta.username = val
	} else {
		return nil, fmt.Errorf("no username given")
	}

	if val, ok := config.AuthParams["password"]; ok {
		meta.password = val
	} else {
		return nil, fmt.Errorf("no password given")
	}

	meta.triggerIndex = config.TriggerIndex
	return &meta, nil
}

func (s *foxScaler) getItemCount(ctx context.Context) (float64, error) {
	var foxResponse1 *foxResponse
	var itemCount float64

	url := s.metadata.host

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return -1, err
	}
	// Add BasicAuth
	req.SetBasicAuth(s.metadata.username, s.metadata.password)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return -1, fmt.Errorf("error sending request to fox-api, %w", err)
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1, err
	}

	err = json.Unmarshal(body, &foxResponse1)
	if err != nil {
		return -1, fmt.Errorf("%w, make sure you enter username, password and collection values correctly in the yaml file", err)
	}

	s.logger.Info("FoxScaler result response = ", foxResponse1.Response.Result)
	itemCount = float64(foxResponse1.Response.Result)
	return itemCount, nil
}

// GetMetricSpecForScaling returns the MetricSpec for the Horizontal Pod Autoscaler
func (s *foxScaler) GetMetricSpecForScaling(context.Context) []v2.MetricSpec {
	externalMetric := &v2.ExternalMetricSource{
		Metric: v2.MetricIdentifier{
			Name: GenerateMetricNameWithIndex(s.metadata.triggerIndex, kedautil.NormalizeString("fox")),
		},
		Target: GetMetricTargetMili(s.metricType, s.metadata.targetQueryValue),
	}
	metricSpec := v2.MetricSpec{
		External: externalMetric, Type: externalMetricType,
	}
	return []v2.MetricSpec{metricSpec}
}

// GetMetricsAndActivity query from Solr,and return to external metrics and activity
func (s *foxScaler) GetMetricsAndActivity(ctx context.Context, metricName string) ([]external_metrics.ExternalMetricValue, bool, error) {
	result, err := s.getItemCount(ctx)
	if err != nil {
		return []external_metrics.ExternalMetricValue{}, false, fmt.Errorf("failed to inspect fox api, because of %w", err)
	}

	metric := GenerateMetricInMili(metricName, result)
	s.logger.Info("FoxScaler GetMetricsAndActivity = ", result > s.metadata.activationTargetQueryValue)

	return append([]external_metrics.ExternalMetricValue{}, metric), result > s.metadata.activationTargetQueryValue, nil
}

// Close closes the http client connection.
func (s *foxScaler) Close(context.Context) error {
	if s.httpClient != nil {
		s.httpClient.CloseIdleConnections()
	}
	return nil
}
