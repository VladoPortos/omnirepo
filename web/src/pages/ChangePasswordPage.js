import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Forced password change page per D-05, TEN-12.
 * Same centered card layout. Banner explaining why.
 */
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { motion } from 'framer-motion';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useAuth } from '@/hooks/useAuth';
import { ApiError } from '@/api/client';
export function ChangePasswordPage() {
    const navigate = useNavigate();
    const { changePassword } = useAuth();
    const [current, setCurrent] = useState('');
    const [newPassword, setNewPassword] = useState('');
    const [confirmPassword, setConfirmPassword] = useState('');
    const [error, setError] = useState('');
    const handleSubmit = async (e) => {
        e.preventDefault();
        setError('');
        if (newPassword !== confirmPassword) {
            setError('Passwords do not match.');
            return;
        }
        if (newPassword.length < 8) {
            setError('Password must be at least 8 characters.');
            return;
        }
        try {
            await changePassword.mutateAsync({
                current,
                new_password: newPassword,
            });
            navigate('/');
        }
        catch (err) {
            if (err instanceof ApiError) {
                setError(err.detail || 'Failed to change password.');
            }
            else {
                setError('Unable to reach the server. Check your connection and try again.');
            }
        }
    };
    return (_jsx("div", { className: "flex min-h-screen items-center justify-center bg-background p-4", children: _jsx(motion.div, { initial: { opacity: 0, scale: 0.95 }, animate: { opacity: 1, scale: 1 }, transition: { duration: 0.15, ease: 'easeOut' }, children: _jsxs(Card, { className: "w-full max-w-md", children: [_jsxs(CardHeader, { className: "text-center", children: [_jsx("div", { className: "mx-auto flex size-12 items-center justify-center rounded-lg bg-primary text-primary-foreground mb-4", children: _jsx("span", { className: "text-xl font-semibold", children: "O" }) }), _jsx(CardTitle, { className: "text-2xl", children: "Change Your Password" })] }), _jsxs(CardContent, { children: [_jsx("div", { className: "mb-4 rounded-md bg-amber-600/10 border border-amber-600/30 p-3 text-sm text-amber-500", children: "Your password must be changed before you can continue." }), _jsxs("form", { onSubmit: handleSubmit, className: "space-y-4", children: [error && (_jsx("div", { className: "rounded-md bg-destructive/10 p-3 text-sm text-destructive", children: error })), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "current-password", children: "Current Password" }), _jsx(Input, { id: "current-password", type: "password", value: current, onChange: (e) => setCurrent(e.target.value), autoComplete: "current-password", required: true })] }), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "new-password", children: "New Password" }), _jsx(Input, { id: "new-password", type: "password", value: newPassword, onChange: (e) => setNewPassword(e.target.value), autoComplete: "new-password", required: true })] }), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "confirm-password", children: "Confirm New Password" }), _jsx(Input, { id: "confirm-password", type: "password", value: confirmPassword, onChange: (e) => setConfirmPassword(e.target.value), autoComplete: "new-password", required: true })] }), _jsx(Button, { type: "submit", className: "w-full", disabled: changePassword.isPending, children: changePassword.isPending ? 'Updating...' : 'Update Password' })] })] })] }) }) }));
}
