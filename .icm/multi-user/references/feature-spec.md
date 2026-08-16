# Feature Specification — Multi-User System

> Layer 3 · Complete functional specification

## 1. Authentication & Registration Workflow
- **Registration**:
  - Endpoint `GET /register` serves signup form (`username`, `password`, `confirm_password`).
  - Endpoint `POST /register` validates inputs (username length >= 3, password length >= 8).
  - Inserts new record into `users` with `status = 'pending'`, `role = 'user'`.
  - Redirects to `/login` with success message stating approval is required.
- **Login**:
  - Endpoint `GET /login` serves login form.
  - Endpoint `POST /login` verifies credentials against:
    1. Static Admin: `ADMIN_USER` and `ADMIN_PASSWORD` from Infisical configuration.
    2. SQLite Database: `users` table via bcrypt comparison.
  - Checks user status:
    - If status is `pending` -> return login page with pending notice.
    - If status is `disabled` -> return login page with disabled notice.
    - If status is `approved` or user is Admin -> generate session token, set cookie, redirect to home.
- **Logout**:
  - Endpoint `POST /logout` removes session record and clears cookie.

## 2. Administrator Management & Badges
- **Navigation & Badge**:
  - Admin tab appears in top navigation when authenticated user has admin privileges.
  - Badge counter displays `SELECT COUNT(*) FROM users WHERE status = 'pending'`.
- **Admin Dashboard (`/admin`)**:
  - Displays table of all registered users (Username, Status, Role, Created At, Action Buttons).
  - Table of pending requests displayed prominently at the top.
  - Actions:
    - `POST /admin/users/{id}/approve`: Sets status to `approved`.
    - `POST /admin/users/{id}/disable`: Sets status to `disabled`.
    - `POST /admin/users/{id}/delete`: Deletes user record and cascades/reassigns songs.

## 3. Song Ownership & Public Sharing
- **Ownership Scoping**:
  - Song generation sets `user_id` on new `jobs` and resulting `songs`.
  - Song detail and delete operations verify requester is the owner or admin.
- **Public Toggle**:
  - Song cards/detail include a toggle switch for `is_public` (0 or 1).
  - Endpoint `POST /songs/{id}/toggle-public` updates visibility.
- **Partitioned Library (`/history`)**:
  - Section 1 ("My Songs"): Lists songs where `user_id = current_user.id`.
  - Section 2 ("Community Songs"): Lists songs where `is_public = 1`.
