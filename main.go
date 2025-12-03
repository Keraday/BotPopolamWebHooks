package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

const apiURL = "https://api.telegram.org/bot"

var token = os.Getenv("TOKEN")
var baseURL = apiURL + token

// хранилище сессий
var (
	sessions = make(map[int64]*Session)
	mu       sync.RWMutex
)

type Expense struct {
	UserID   int64
	Username string
	Amount   float64
}

type Session struct {
	Expenses []Expense
	Users    map[int64]string
}

type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	From      User   `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type SendMessageRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

func main() {
	if token == "" {
		log.Fatal("TOKEN не установлен.")
	}

	// Проверка токена
	resp, err := http.Get(baseURL + "/getMe")
	if err != nil || resp.StatusCode != http.StatusOK {
		log.Fatal("Неверный токен.")
	}
	resp.Body.Close()

	webhookURL := os.Getenv("URL")
	if webhookURL == "" {
		log.Fatal("URL не установлен.")
	}
	err = setWebhook(webhookURL)
	if err != nil {
		log.Fatalf("Не удалось установить webhook: %v", err)
	}
	log.Printf("Webhook установле: %s", webhookURL)

	http.HandleFunc("/bot", webhookHandler)
	port := os.Getenv("PORT")
	if port == "" {
		port = "443"
	}
	log.Printf("Сервер запущен на порту %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))

}

// устанавливает вебхук
func setWebhook(url string) error {
	reqBody := fmt.Sprintf(`{"url":"%s"}`, url)
	resp, err := http.Post(baseURL+"/setWebhook", "application/json", strings.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}

	json.Unmarshal(body, &result)
	if !result["ok"].(bool) {
		return fmt.Errorf("не удалось установить webhook: %s", result["description"])
	}
	return nil
}

// обработчик вебхука
func webhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "только POST метод", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("не удалось прочитать тело запроса: %v", err)
		return
	}
	defer r.Body.Close()

	var update Update
	if err := json.Unmarshal(body, &update); err != nil {
		log.Printf("не удалось разобрать JSON: %v", err)
		return
	}
	if update.Message != nil {
		handleMessage(update.Message)
	}

	w.WriteHeader(http.StatusOK)
}

// func getUpdates(offset int) ([]Update, error) {
// 	url := fmt.Sprintf("%s/getUpdates?offset=%d&timeout=30", baseURL, offset)
// 	resp, err := http.Get(url)
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer resp.Body.Close()

// 	body, _ := io.ReadAll(resp.Body)
// 	var result struct {
// 		Ok     bool     `json:"ok"`
// 		Result []Update `json:"result"`
// 	}
// 	if err := json.Unmarshal(body, &result); err != nil {
// 		return nil, fmt.Errorf("ошибка разбора JSON: %w", err)
// 	}

// 	if !result.Ok {
// 		return nil, fmt.Errorf("API error: %s", string(body))
// 	}
// 	return result.Result, nil
// }

func sendMessage(chatID int64, text string) {
	req := SendMessageRequest{ChatID: chatID, Text: text}
	jsonData, _ := json.Marshal(req)
	http.Post(baseURL+"/sendMessage", "application/json", bytes.NewBuffer(jsonData))
}

func getOrCreateSession(chatID int64) *Session {
	mu.Lock()
	defer mu.Unlock()
	if s, ok := sessions[chatID]; ok {
		return s
	}
	s := &Session{
		Expenses: []Expense{},
		Users:    make(map[int64]string),
	}
	sessions[chatID] = s
	return s
}

func handleMessage(msg *Message) {
	chatID := msg.Chat.ID
	userID := msg.From.ID
	username := msg.From.Username
	if username == "" {
		username = msg.From.FirstName
	}

	session := getOrCreateSession(chatID)
	session.Users[userID] = username

	text := strings.TrimSpace(msg.Text)
	text = strings.ToLower(text)
	if text == "" {
		return
	}

	switch {
	case text == "/start":
		sendMessage(chatID, "Привет! Я бот для расчёта общих расходов.\n\n"+
			"Используй:\n"+
			"/add <сумма> — добавить твою покупку\n"+
			"/calc — посчитать, кто кому должен\n"+
			"/reset — сбросить все данные")

	case strings.HasPrefix(text, "/add "):
		parts := strings.Fields(text)
		if len(parts) < 2 {
			sendMessage(chatID, "Используй: /add 1500")
			return
		}
		amount, err := strconv.ParseFloat(parts[1], 64)
		if err != nil || amount <= 0 {
			sendMessage(chatID, "Сумма должна быть положительным числом.")
			return
		}
		session.Expenses = append(session.Expenses, Expense{
			UserID:   userID,
			Username: username,
			Amount:   amount,
		})
		var totalAmountU float64
		for _, sum := range session.Expenses {
			if sum.UserID == userID {
				totalAmountU += sum.Amount
			}

		}

		sendMessage(chatID, fmt.Sprintf("Добавлено: %s потратил(а) %.2f, всего: %.2f", username, amount, totalAmountU))

	case text == "/calc":
		if len(session.Expenses) == 0 {
			sendMessage(chatID, "Нет данных. Добавь расходы через /add")
			return
		}

		// считаем общую сумму и сколько заплатил каждый
		total := 0.0
		paid := make(map[int64]float64)
		for _, e := range session.Expenses {
			total += e.Amount
			paid[e.UserID] += e.Amount
		}

		userIDs := make([]int64, 0, len(paid))
		for uid := range paid {
			userIDs = append(userIDs, uid)
		}
		perPerson := total / float64(len(userIDs))

		// кто сколько должен (отрицательно = должен, положительно = переплатил)
		balance := make(map[int64]float64)
		for _, uid := range userIDs {
			balance[uid] = paid[uid] - perPerson
		}

		// алгоритм расчёта переводов
		var debtors []struct {
			id     int64
			amount float64
		}
		creditors := []struct {
			id     int64
			amount float64
		}{}

		for uid, bal := range balance {
			if bal < -0.01 { // должен
				debtors = append(debtors, struct {
					id     int64
					amount float64
				}{uid, -bal})
			} else if bal > 0.01 { // переплатил
				creditors = append(creditors, struct {
					id     int64
					amount float64
				}{uid, bal})
			}
		}

		if len(debtors) == 0 && len(creditors) == 0 {
			sendMessage(chatID, "Все расходы уже равны!")
			return
		}

		var result strings.Builder
		result.WriteString("Расчёт:\n\n")

		// жадный-алгоритм
		i, j := 0, 0
		for i < len(debtors) && j < len(creditors) {
			d := &debtors[i]
			c := &creditors[j]
			amount := math.Min(d.amount, c.amount)
			d.amount -= amount
			c.amount -= amount

			result.WriteString(fmt.Sprintf(
				"%s → %s: %.2f\n",
				session.Users[d.id],
				session.Users[c.id],
				amount,
			))

			if d.amount < 0.01 {
				i++
			}
			if c.amount < 0.01 {
				j++
			}
		}

		sendMessage(chatID, result.String())

	case text == "/reset":
		mu.Lock()
		delete(sessions, chatID)
		mu.Unlock()
		sendMessage(chatID, "Все данные сброшены.")

	default:
		sendMessage(chatID, "Неизвестная команда. Используй /start")
	}
}
