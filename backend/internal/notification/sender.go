package notification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
)

type BuildEvent struct {
	AppName     string
	Branch      string
	Status      string
	TriggerUser string
	Duration    int
	ErrorStage  string
	ErrorMsg    string
}

// SendBuildNotification sends notifications for a build event
func SendBuildNotification(event BuildEvent) {
	repo := repository.NewNotificationRepo()
	webhooks, err := repo.FindEnabled()
	if err != nil || len(webhooks) == 0 {
		return
	}

	for _, wh := range webhooks {
		if event.Status == "SUCCESS" && !wh.NotifyOnSuccess {
			continue
		}
		if event.Status == "FAILED" && !wh.NotifyOnFailure {
			continue
		}

		go func(webhook model.NotificationWebhook) {
			var sendErr error
			switch webhook.Type {
			case "dingtalk":
				sendErr = sendDingTalk(webhook, event)
			case "wecom":
				sendErr = sendWeCom(webhook, event)
			}
			if sendErr != nil {
				logger.Log.Errorf("Notification send failed (%s): %v", webhook.Name, sendErr)
				// Retry once after 30s
				time.AfterFunc(30*time.Second, func() {
					switch webhook.Type {
					case "dingtalk":
						sendDingTalk(webhook, event)
					case "wecom":
						sendWeCom(webhook, event)
					}
				})
			}
		}(wh)
	}
}

func sendDingTalk(wh model.NotificationWebhook, event BuildEvent) error {
	statusEmoji := "✅"
	if event.Status == "FAILED" {
		statusEmoji = "❌"
	}

	content := fmt.Sprintf("%s 构建通知\n\n应用: %s\n分支: %s\n状态: %s\n触发人: %s\n耗时: %ds",
		statusEmoji, event.AppName, event.Branch, event.Status, event.TriggerUser, event.Duration)

	if event.ErrorStage != "" {
		content += fmt.Sprintf("\n失败阶段: %s\n错误: %s", event.ErrorStage, event.ErrorMsg)
	}

	payload := map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": content},
	}

	return postWebhook(wh.WebhookURL, payload)
}

func sendWeCom(wh model.NotificationWebhook, event BuildEvent) error {
	statusEmoji := "✅"
	if event.Status == "FAILED" {
		statusEmoji = "❌"
	}

	content := fmt.Sprintf("%s 构建通知\n\n应用: %s\n分支: %s\n状态: %s\n触发人: %s\n耗时: %ds",
		statusEmoji, event.AppName, event.Branch, event.Status, event.TriggerUser, event.Duration)

	if event.ErrorStage != "" {
		content += fmt.Sprintf("\n失败阶段: %s\n错误: %s", event.ErrorStage, event.ErrorMsg)
	}

	payload := map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": content},
	}

	return postWebhook(wh.WebhookURL, payload)
}

func postWebhook(url string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	logger.Log.Infof("Notification sent to %s", url)
	return nil
}

// SendTestMessage sends a test message to a webhook
func SendTestMessage(wh *model.NotificationWebhook) error {
	event := BuildEvent{
		AppName:     "test-app",
		Branch:      "master",
		Status:      "SUCCESS",
		TriggerUser: "system",
		Duration:    10,
	}

	switch wh.Type {
	case "dingtalk":
		return sendDingTalk(*wh, event)
	case "wecom":
		return sendWeCom(*wh, event)
	default:
		return fmt.Errorf("unsupported webhook type: %s", wh.Type)
	}
}
