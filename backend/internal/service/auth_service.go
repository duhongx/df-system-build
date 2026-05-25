package service

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"

	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	userRepo   *repository.UserRepo
	codeMu     sync.RWMutex
	verifyCodes map[string]verifyCode
}

type verifyCode struct {
	Code      string
	ExpiresAt time.Time
}

func NewAuthService() *AuthService {
	return &AuthService{
		userRepo:    repository.NewUserRepo(),
		verifyCodes: make(map[string]verifyCode),
	}
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type LoginResponse struct {
	Token     string     `json:"token"`
	ExpiresAt time.Time  `json:"expiresAt"`
	User      model.User `json:"user"`
}

func (s *AuthService) Login(req *LoginRequest) (*LoginResponse, error) {
	user, err := s.userRepo.FindByUsername(req.Username)
	if err != nil {
		return nil, errors.New("用户名或密码错误")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, errors.New("用户名或密码错误")
	}

	token, err := middleware.GenerateToken(user.ID, user.Username)
	if err != nil {
		return nil, errors.New("生成令牌失败")
	}

	return &LoginResponse{
		Token:     token,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		User:      *user,
	}, nil
}

func (s *AuthService) GetProfile(userID uint) (*model.User, error) {
	return s.userRepo.FindByID(userID)
}

type UpdateProfileRequest struct {
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Department string `json:"department"`
}

func (s *AuthService) UpdateProfile(userID uint, req *UpdateProfileRequest) (*model.User, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return nil, errors.New("用户不存在")
	}

	user.Email = req.Email
	user.Phone = req.Phone
	user.Department = req.Department

	if err := s.userRepo.Update(user); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *AuthService) SendVerifyCode(email string) error {
	code := fmt.Sprintf("%06d", rand.Intn(1000000))

	s.codeMu.Lock()
	s.verifyCodes[email] = verifyCode{
		Code:      code,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	s.codeMu.Unlock()

	// Log the code (in production, send via SMTP)
	logger.Log.Infof("Verify code for %s: %s", email, code)

	// For now, just log it. SMTP integration requires:
	// - SMTP server config (host, port, username, password)
	// - Add to config.yaml and implement net/smtp.SendMail
	// The code is stored in memory and can be used for password change.

	return nil
}

type ChangePasswordRequest struct {
	Email           string `json:"email" binding:"required"`
	Code            string `json:"code" binding:"required"`
	NewPassword     string `json:"newPassword" binding:"required,min=6"`
	ConfirmPassword string `json:"confirmPassword" binding:"required"`
}

func (s *AuthService) ChangePassword(userID uint, req *ChangePasswordRequest) error {
	if req.NewPassword != req.ConfirmPassword {
		return errors.New("两次密码不一致")
	}

	// Verify code
	s.codeMu.RLock()
	vc, exists := s.verifyCodes[req.Email]
	s.codeMu.RUnlock()

	if !exists || vc.Code != req.Code {
		return errors.New("验证码错误")
	}
	if time.Now().After(vc.ExpiresAt) {
		return errors.New("验证码已过期")
	}

	// Hash new password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return errors.New("密码加密失败")
	}

	if err := s.userRepo.UpdatePassword(userID, string(hash)); err != nil {
		return err
	}

	// Clear the "must change" flag
	s.userRepo.SetMustChangePassword(userID, false)

	// Remove used code
	s.codeMu.Lock()
	delete(s.verifyCodes, req.Email)
	s.codeMu.Unlock()

	return nil
}
